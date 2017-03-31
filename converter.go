package amphtml

import (
	"bytes"
	"crypto/sha1"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/favclip/ampassador/amppb"
	"github.com/favclip/html2html"
	"github.com/tdewolff/minify"
	"github.com/tdewolff/minify/css"
)

type TagClassStyleMap map[string]map[string]string

type Option interface {
	implements(conv *Converter)
}

type withCanonicalURLOption struct {
	canonicalURL string
}

func (o *withCanonicalURLOption) implements(conv *Converter) {
	conv.canonicalURL = o.canonicalURL
}

func WithCanonicalURL(canonicalURL string) Option {
	return &withCanonicalURLOption{canonicalURL: canonicalURL}
}

type withFileFetcherOption struct {
	fileFetcher FileFetcher
}

func (o *withFileFetcherOption) implements(conv *Converter) {
	conv.fileFetcher = o.fileFetcher
}

func WithFileFetcher(fileFetcher FileFetcher) Option {
	return &withFileFetcherOption{fileFetcher: fileFetcher}
}

type withAMPImageStatsFetcherOption struct {
	ampImageStatsFetcher AMPImageStatsFetcher
}

func (o *withAMPImageStatsFetcherOption) implements(conv *Converter) {
	conv.ampImageStatsFetcher = o.ampImageStatsFetcher
}

func WithAMPImageStatsFetcher(ampImageStatsFetcher AMPImageStatsFetcher) Option {
	return &withAMPImageStatsFetcherOption{ampImageStatsFetcher: ampImageStatsFetcher}
}

type Converter struct {
	debug bool

	canonicalURL         string
	fileFetcher          FileFetcher
	ampImageStatsFetcher AMPImageStatsFetcher

	ampValidatorRules *wrappedRules

	requires     map[string]*amppb.TagSpec
	satisfied    map[string]*amppb.TagSpec
	tagSpecReady map[*amppb.TagSpec][]html2html.Tag

	ampErrors AMPErrors
}

func NewConverter(opts ...Option) (*Converter, error) {
	// TODO move to options
	b, err := ioutil.ReadFile("./amppb/validator-main.protoascii")
	if err != nil {
		return nil, err
	}
	rules, err := newWrappedRules(string(b))
	if err != nil {
		return nil, err
	}
	conv := &Converter{
		debug:             true,
		canonicalURL:      "/",
		ampValidatorRules: rules,
		requires:          make(map[string]*amppb.TagSpec),
		satisfied:         make(map[string]*amppb.TagSpec),
		tagSpecReady:      make(map[*amppb.TagSpec][]html2html.Tag),
	}
	for _, opt := range opts {
		opt.implements(conv)
	}

	if conv.fileFetcher == nil {
		canonicalURL, err := url.Parse(conv.canonicalURL)
		if err != nil {
			return nil, err
		}
		if canonicalURL.Host == "" {
			return nil, errors.New("FileFetcher is required")
		}

		conv.fileFetcher = func(targetURL *url.URL) (io.ReadCloser, error) {
			if targetURL.Host == "" {
				targetURL.Host = canonicalURL.Host

				if !strings.HasPrefix(targetURL.Path, "/") {
					// TODO base url?
					targetURL.Path = path.Join(canonicalURL.Path, targetURL.Path)
				}
			}

			resp, err := http.Get(targetURL.String())
			if err != nil {
				return nil, err
			}
			return resp.Body, nil
		}
	}
	if conv.ampImageStatsFetcher == nil {
		conv.ampImageStatsFetcher = &ampImageStatsFetcherImpl{fileFetcher: conv.fileFetcher}
	}

	return conv, nil
}

func (conv *Converter) ReplaceToAMPTag(tag html2html.Tag) (html2html.Tag, TagClassStyleMap, error) {
	styleMap := make(TagClassStyleMap)
	var linkCSSContents []string

	type Modifier func(tag html2html.Tag) (html2html.Token, error)

	childModifier := func(modifier Modifier, tag html2html.Tag) error {
		for _, token := range tag.Tokens() {
			if token.Type() != html2html.TypeTagToken {
				continue
			}

			child := token.Tag()
			childAltTag, err := html2html.TagReplacer(child, modifier)
			if err != nil {
				return err
			} else if childAltTag != nil {
				tag.ReplateChildToken(child, childAltTag)
			}
		}

		return nil
	}

	var modifier Modifier
	modifier = func(tag html2html.Tag) (html2html.Token, error) {
		if tag.IsDocumentRoot() {
			err := childModifier(modifier, tag)
			if err != nil {
				return nil, err
			}
			return tag, nil
		}

		processed := false

		// extract css informations
		if isLinkStyleSheet(tag) {
			hrefAttr := tag.GetAttr("href")
			if hrefAttr == nil {
				if conv.debug {
					return html2html.CreateCommentToken("replaced: link tag. href attr is not found"), nil
				}
				return html2html.CreateTextToken(""), nil
			}
			styleSheetURL, err := url.Parse(hrefAttr.Value)
			if err != nil {
				return nil, err
			}
			f, err := conv.fileFetcher(styleSheetURL)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			b, err := ioutil.ReadAll(f)
			if err != nil {
				return nil, err
			}

			var content string
			if conv.debug {
				content += fmt.Sprintf("/* from %s */\n", hrefAttr.Value)
			}
			content += string(b)
			linkCSSContents = append(linkCSSContents, content)

			if conv.debug {
				return html2html.CreateCommentToken(fmt.Sprintf(" replaced: link tag %s ", hrefAttr.Value)), nil
			}
			return html2html.CreateTextToken(""), nil
		}
		if isEmbedStyleSheet(tag) {
			buf := bytes.NewBufferString("")
			if conv.debug {
				buf.WriteString("/* from style tag */\n")
			}
			for _, token := range tag.Tokens() {
				token.BuildHTML(buf)
			}
			linkCSSContents = append(linkCSSContents, buf.String())

			if conv.debug {
				return html2html.CreateCommentToken(" replaced: style tag "), nil
			}
			return html2html.CreateTextToken(""), nil
		}

		// replace to amp-* tags
		for _, ampTag := range AMPTags {
			if tag.Name() != ampTag.SrcTag {
				continue
			}

			altTag, err := ampTag.Modifier(conv, ampTag, tag)
			if err != nil {
				return nil, err
			}

			tag = altTag
			processed = true
			break
		}

		// custom elements handling
		if strings.Contains(tag.Name(), "-") && !AMPTags.isAMPTag(tag) {

			altTag := html2html.CreateElement("div")

			var classAttr string
			for _, attr := range tag.Attrs() {
				if attr.Key == "class" {
					classAttr = attr.Value
					continue
				}

				altTag.AddAttr(attr.Key, attr.Value)
			}

			if classAttr != "" {
				classAttr += " "
			}
			classAttr += tag.Name()
			altTag.AddAttr("class", classAttr)

			altTag.AddChildTokens(tag.Tokens()...)
			tag = altTag

			processed = true
		}

		// check tag name validity
		if conv.ampValidatorRules.countTagSpecs(tag.Name()) != 0 {
			processed = true
		}
		/*
			if len(conv.tagMatchedSpecs(tag)) != 0 || conv.willBeReuse(tag) {
				processed = true
			}
		*/

		if !processed {
			// disallowed tag incoming
			if conv.debug {
				return html2html.CreateCommentToken(fmt.Sprintf(" removed: %s tag ", tag.Name())), nil
			}
			return html2html.CreateTextToken(""), nil
		}

		// convert style to class
		var styleAttr string
		var classAttr string
		{
			newAttrs := make([]*html2html.Attr, 0, len(tag.Attrs()))
			for _, attr := range tag.Attrs() {
				if attr.Key == "style" {
					styleAttr = attr.Value
					processed = true
					continue
				} else if attr.Key == "class" {
					classAttr = attr.Value
					processed = true
					continue
				}
				newAttrs = append(newAttrs, attr)
			}
			tag.SetAttrs(newAttrs)
		}

		if styleAttr != "" {
			h := sha1.New()
			h.Write([]byte(styleAttr))
			sha1Hash := fmt.Sprintf("%x", h.Sum(nil))
			sha1Hash = sha1Hash[0:10]
			className := "h2a-" + tag.Name() + "-" + sha1Hash
			if styleMap[tag.Name()] == nil {
				styleMap[tag.Name()] = make(map[string]string)
			}
			styleMap[tag.Name()][className] = styleAttr

			if classAttr != "" {
				classAttr += " "
			}
			classAttr += className
		}
		if classAttr != "" {
			tag.AddAttr("class", classAttr)
		}

		// check attr validity
		for _, tagSpec := range conv.tagMatchedSpecs(tag) {
			// NOTE don't check tag specs. It will be check in later.
			// but checking up the attr specs...

			for _, attrSpec := range conv.ampValidatorRules.getAttrSpecs(tagSpec) {
				err := conv.replaceTagAttr(tag, tagSpec, attrSpec)
				if err != nil {
					return nil, err
				}
			}
		}

		// TODO remove unnecessary attr

		if processed {
			err := childModifier(modifier, tag)
			if err != nil {
				return nil, err
			}

			return tag, nil
		}

		return nil, nil
	}

	altToken, err := html2html.TagReplacer(tag, modifier)
	if err != nil {
		return nil, nil, err
	}

	if len(linkCSSContents) != 0 {
		if styleMap[""] == nil {
			styleMap[""] = make(map[string]string)
		}
		styleMap[""][""] = strings.Join(linkCSSContents, "\n")
	}

	if altToken != nil {
		if altToken.Type() != html2html.TypeTagToken {
			return nil, nil, errors.New("unexpected state")
		}
		tag = altToken.Tag()
	}

	return tag, styleMap, nil
}

func (conv *Converter) replaceTagAttr(tag html2html.Tag, tagSpec *amppb.TagSpec, attrSpec *amppb.AttrSpec) error {
	if attrSpec.GetName() == "" {
		return errors.New("unknown attrSpec name")
	}

	for _, altName := range attrSpec.GetAlternativeNames() {
		if tag.HasAttr(altName) {
			// NOTE: Currently there is no data to refer to each other
			return nil
		}
	}

	if attrSpec.GetMandatory() && !tag.HasAttr(attrSpec.GetName()) {
		if attrSpec.Value != nil {
			tag.AddAttr(attrSpec.GetName(), attrSpec.GetValue())
		} else if attrSpec.ValueCasei != nil {
			tag.AddAttr(attrSpec.GetName(), attrSpec.GetValueCasei())
		} else {
			conv.addAMPError(&AMPError{
				Type:                AMPValidatorError,
				token:               tag,
				validatorSourceSpec: tagSpec,
				cause:               attrSpec,
			})
			return nil
		}
	}

	// TODO support mandatory_oneof

	// remove first
	if attrSpec.ValueRegex != nil && tag.HasAttr(attrSpec.GetName()) {
		re, err := regexp.Compile(attrSpec.GetValueRegex())
		if err != nil {
			return err
		}
		if !re.MatchString(tag.GetAttr(attrSpec.GetName()).Value) {
			tag.RemoveAttr(attrSpec.GetName())

			conv.addAMPError(&AMPError{
				Type:                AMPRemoveAttr,
				token:               tag,
				validatorSourceSpec: tagSpec,
				cause:               attrSpec,
			})
		}
	}
	if attrSpec.ValueRegexCasei != nil && tag.HasAttr(attrSpec.GetName()) {
		re, err := regexp.Compile("(?i)" + attrSpec.GetValueRegexCasei())
		if err != nil {
			return err
		}
		if !re.MatchString(tag.GetAttr(attrSpec.GetName()).Value) {
			tag.RemoveAttr(attrSpec.GetName())

			conv.addAMPError(&AMPError{
				Type:                AMPRemoveAttr,
				token:               tag,
				validatorSourceSpec: tagSpec,
				cause:               attrSpec,
			})
		}
	}
	if attrSpec.BlacklistedValueRegex != nil && tag.HasAttr(attrSpec.GetName()) {
		re, err := regexp.Compile("(?i)" + attrSpec.GetBlacklistedValueRegex())
		if err != nil {
			return err
		}

		if !re.MatchString(tag.GetAttr(attrSpec.GetName()).Value) {
			tag.RemoveAttr(attrSpec.GetName())

			conv.addAMPError(&AMPError{
				Type:                AMPRemoveAttr,
				token:               tag,
				validatorSourceSpec: tagSpec,
				cause:               attrSpec,
			})
		}
	}

	if attrSpec.Value != nil && !tag.HasAttrValue(attrSpec.GetName(), attrSpec.GetValue()) {
		conv.addAMPError(&AMPError{
			Type:                AMPValidatorError,
			token:               tag,
			validatorSourceSpec: tagSpec,
			cause:               attrSpec,
		})
		return nil
	}

	if attrSpec.ValueCasei != nil && !tag.HasAttrValueCaseInsensitive(attrSpec.GetName(), attrSpec.GetValueCasei()) {
		conv.addAMPError(&AMPError{
			Type:                AMPValidatorError,
			token:               tag,
			validatorSourceSpec: tagSpec,
			cause:               attrSpec,
		})
		return nil
	}

	if attrSpec.ValueUrl != nil && tag.HasAttr(attrSpec.GetName()) {
		attr := tag.GetAttr(attrSpec.GetName())
		attrURL, err := url.Parse(attr.Value)
		if err != nil {
			return err
		}
		urlSpec := attrSpec.GetValueUrl()
		if urlSpec.GetAllowRelative() && !strings.HasPrefix(attrURL.Path, "/") {
			// ok
		} else if len(urlSpec.GetAllowedProtocol()) != 0 {
			found := false

			for _, allowedProtocol := range urlSpec.AllowedProtocol {
				if attrURL.Scheme == allowedProtocol {
					found = true
					break
				}
			}

			if !found {

			}
		}

		if !urlSpec.GetAllowEmpty() && attr.Value == "" {
			conv.addAMPError(&AMPError{
				Type:                AMPValidatorError,
				token:               tag,
				validatorSourceSpec: tagSpec,
				cause:               attrSpec,
			})
		}

		for _, disallowed := range urlSpec.GetDisallowedDomain() {
			if attrURL.Host == disallowed {
				conv.addAMPError(&AMPError{
					Type:                AMPValidatorError,
					token:               tag,
					validatorSourceSpec: tagSpec,
					cause:               attrSpec,
				})
				break
			}
		}
	}

	if attrSpec.ValueProperties != nil && tag.HasAttr(attrSpec.GetName()) {
		attr := tag.GetAttr(attrSpec.GetName())
		valueMap := parsePropertiesValue(attr.Value)
		for _, propSpec := range attrSpec.GetValueProperties().GetProperties() {
			if v, ok := valueMap[propSpec.GetName()]; ok {
				if propSpec.Value != nil && propSpec.GetValue() != v {
					valueMap[propSpec.GetName()] = propSpec.GetValue()
				}
				if propSpec.ValueDouble != nil {
					v, err := strconv.ParseFloat(attr.Value, 64)
					if err != nil {
						return err
					}
					if propSpec.GetValueDouble() != v {
						valueMap[propSpec.GetName()] = fmt.Sprintf("%g", propSpec.GetValueDouble())
					}
				}
			} else if propSpec.GetMandatory() {
				if propSpec.Value != nil {
					valueMap[propSpec.GetName()] = propSpec.GetValue()
				} else if propSpec.ValueDouble != nil {
					valueMap[propSpec.GetName()] = fmt.Sprintf("%g", propSpec.GetValueDouble())
				}
			}
		}
	}

	// TODO support Trigger

	if attrSpec.Deprecation != nil {
		conv.addAMPError(&AMPError{
			Type:                AMPDeprecation,
			token:               tag,
			validatorSourceSpec: tagSpec,
			cause:               attrSpec,
		})
	}

	// TODO support DispatchKey
	// 親タグと同名のタグが既にあったらエラーにする的な…？

	// TODO support Implicit

	return nil
}

func (conv *Converter) MakeUpRequiredTags(tag html2html.Tag) html2html.Tag {
	// check html tag
	rootTag := tag
	if htmlTags := tag.GetElementsByTagName("html"); len(htmlTags) == 0 {
		rootTag = html2html.CreateDocumentRoot()
		rootTag.AddChildTokens(html2html.CreateDoctypeToken("html"))
		htmlTag := html2html.CreateElement("html")
		htmlTag.AddAttr("⚡", "")
		rootTag.AddChildTokens(htmlTag)

		bodyTags := tag.GetElementsByTagName("body")
		if len(bodyTags) != 0 {
			htmlTag.AddChildTokens(tag)
		} else {
			bodyTag := html2html.CreateElement("body")
			htmlTag.AddChildTokens(bodyTag)
			bodyTag.AddChildTokens(tag)
		}
	}

	// check head tag
	if headTags := rootTag.GetElementsByTagName("head"); len(headTags) == 0 {
		htmlTag := rootTag.GetElementsByTagName("html")[0]
		htmlTag.UnshiftChileToken(html2html.CreateElement("head"))
	}

	// check body tag
	if bodyTags := rootTag.GetElementsByTagName("body"); len(bodyTags) == 0 {
		htmlTag := rootTag.GetElementsByTagName("html")[0]
		htmlTag.AddChildTokens(html2html.CreateElement("body"))
	}

	return rootTag
}

func (conv *Converter) StyleToAMPCustomTag(styleMap TagClassStyleMap) (html2html.Token, error) {
	if len(styleMap) == 0 {
		return html2html.CreateTextToken(""), nil
	}

	buf := bytes.NewBufferString("")
	for tagName, classStyleMap := range styleMap {
		for className, style := range classStyleMap {
			if tagName == "" && className == "" {
				// from link tag
				buf.WriteString(style)
				buf.WriteString("\n")
				continue
			}
			if tagName != "" {
				buf.WriteString(tagName)
			}
			if className != "" {
				buf.WriteString(".")
				buf.WriteString(className)
				buf.WriteString("{")
				buf.WriteString(style)
				buf.WriteString("}\n")
			}
		}
	}

	style := html2html.CreateElement("style")
	style.AddAttr("amp-custom", "")

	styleString := buf.String()

	if 50000 < buf.Len() {
		m := minify.New()
		m.AddFunc("text/css", css.Minify)
		cssStr, err := m.String("text/css", styleString)
		if err != nil {
			return nil, err
		}
		if 50000 < len(cssStr) {
			// TODO AMP error
		}
		style.AddChildTokens(html2html.CreateTextToken(cssStr))
	} else {
		style.AddChildTokens(html2html.CreateTextToken(buf.String()))
	}

	return style, nil
}

func (conv *Converter) FittingToAMPSpecs(rootTag html2html.Tag) error {
	for _, tagSpec := range conv.ampValidatorRules.rules.GetTags() {
		err := conv.FittingToAMPSpec(tagSpec, rootTag)
		if err != nil {
			return err
		}
	}

	return nil
}

func (conv *Converter) FittingToAMPSpec(tagSpec *amppb.TagSpec, rootTag html2html.Tag) error {
	if htmlFormats := tagSpec.GetHtmlFormat(); len(htmlFormats) != 0 {
		found := false
		for _, htmlFormat := range htmlFormats {
			if htmlFormat == conv.ampValidatorRules.targetHTMLFormat {
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}

	if tagSpec.GetMandatory() || (tagSpec.MandatoryAlternatives != nil && tagSpec.GetMandatoryAlternatives() == tagSpec.GetSpecName()) {
		if v := tagSpec.GetTagName(); v == "!DOCTYPE" {
			doctypeToken := rootTag.Tokens()[0]
			if doctypeToken.Type() != html2html.TypeDoctypeToken {
				doctypeToken = html2html.CreateDoctypeToken("html")
				rootTag.UnshiftChileToken(doctypeToken)
			} else if doctypeToken.TextToken().Text() != "html" {
				rootTag.ReplateChildToken(doctypeToken, html2html.CreateDoctypeToken("html"))
			}
			return nil
		}

		matchTags := conv.specMatchedTags(tagSpec, rootTag)
		if len(matchTags) == 0 {
			targetTag := conv.findReusableTag(tagSpec, rootTag)

			if targetTag != nil {
				for _, attrSpec := range conv.ampValidatorRules.getAttrSpecs(tagSpec) {
					err := conv.replaceTagAttr(targetTag, tagSpec, attrSpec)
					if err != nil {
						return err
					}
				}

				matchTags = conv.specMatchedTags(tagSpec, rootTag)
			}
		}

		if tagSpec.GetSpecName() == "noscript \u003e style[amp-boilerplate]" {
			// nosciptrの中身のstyleは "noscript enclosure for boilerplate" で中身毎生成されるので無視する
		} else if len(matchTags) == 0 {
			token := conv.tagSpecToToken(tagSpec)
			conv.insertTagByTagSpec(rootTag, tagSpec, token)
		}

		// TODO support MandatoryAlternatives
	}

	matchTags := conv.specMatchedTags(tagSpec, rootTag)

	if tagSpec.GetUnique() && 1 < len(matchTags) {
		conv.addAMPError(&AMPError{
			Type:                AMPValidatorError,
			token:               rootTag,
			validatorSourceSpec: tagSpec,
			cause:               tagSpec,
		})
	} else if tagSpec.GetUniqueWarning() && 1 < len(matchTags) {
		conv.addAMPError(&AMPError{
			Type:                AMPValidatorWarning,
			token:               rootTag,
			validatorSourceSpec: tagSpec,
			cause:               tagSpec,
		})
	}

	if len(matchTags) == 0 {
		return nil
	}

	for _, tag := range matchTags {
		for _, disallowedAncestor := range tagSpec.GetDisallowedAncestor() {
			ancestor := tag.FindAncestor(disallowedAncestor)
			if ancestor != nil {
				conv.addAMPError(&AMPError{
					Type:                AMPValidatorError,
					token:               tag,
					validatorSourceSpec: tagSpec,
					cause:               tagSpec,
				})
			}
		}

		for _, attrSpec := range conv.ampValidatorRules.getAttrSpecs(tagSpec) {
			if attrSpec.GetName() == "" {
				return errors.New("unknown attrSpec name")
			}

			if attrSpec.GetMandatory() && !tag.HasAttr(attrSpec.GetName()) {
				conv.addAMPError(&AMPError{
					Type:                AMPValidatorError,
					token:               tag,
					validatorSourceSpec: tagSpec,
					cause:               attrSpec,
				})
			}

			// TODO support MandatoryOneof, Value, ValueCasei, ValueRegex, ValueRegexCasei, ValueUrl, ValueProperties, BlacklistedValueRegex

			// TODO Cdata
			// TODO ChildTags
			// TODO AmpLayout
		}

		findAttrSpec := func(attr *html2html.Attr) *amppb.AttrSpec {
			for _, attrSpec := range conv.ampValidatorRules.getAttrSpecs(tagSpec) {
				if attrSpec.GetName() == attr.Key {
					return attrSpec
				}
				for _, altAttrKey := range attrSpec.GetAlternativeNames() {
					if altAttrKey == attr.Key {
						return attrSpec
					}
				}
			}

			return nil
		}

		for _, attr := range tag.Attrs() {
			attrSpec := findAttrSpec(attr)
			if attrSpec == nil {
				tag.RemoveAttr(attr.Key)
				continue
			}
		}

		for _, satisfied := range tagSpec.GetSatisfies() {
			conv.satisfied[satisfied] = tagSpec
		}

		for _, required := range tagSpec.GetRequires() {
			conv.requires[required] = tagSpec
		}

		if tagSpec.Deprecation != nil {
			conv.addAMPError(&AMPError{
				Type:                AMPDeprecation,
				token:               tag,
				validatorSourceSpec: tagSpec,
				cause:               tagSpec,
			})
		}
	}

	return nil
}

func (conv *Converter) ConvertToFullHTML(tag html2html.Tag) (html2html.Tag, error) {

	tag, styleMap, err := conv.ReplaceToAMPTag(tag)
	if err != nil {
		return nil, err
	}

	rootTag := conv.MakeUpRequiredTags(tag)
	if !tag.IsDocumentRoot() {
		return nil, errors.New("unexpected state, root is not document root")
	}

	style, err := conv.StyleToAMPCustomTag(styleMap)
	if err != nil {
		return nil, err
	}
	rootTag.GetElementsByTagName("head")[0].AddChildTokens(style)

	err = conv.FittingToAMPSpecs(rootTag)
	if err != nil {
		return nil, err
	}

	// TODO verify Requires

	err = nil
	if len(conv.ampErrors) != 0 {
		// TODO
		// err = conv.ampErrors
	}

	return rootTag, err
}

func (conv *Converter) addAMPError(ampError *AMPError) {
	conv.ampErrors = append(conv.ampErrors, ampError)
}

func (conv *Converter) tagMatchedSpecs(tag html2html.Tag) []*amppb.TagSpec {
	var resultList []*amppb.TagSpec
	for _, tagSpec := range conv.ampValidatorRules.rules.GetTags() {
		if !conv.isSpecMatchedTag(tagSpec, tag) {
			continue
		}

		resultList = append(resultList, tagSpec)
	}

	return resultList
}

func (conv *Converter) specMatchedTags(tagSpec *amppb.TagSpec, rootTag html2html.Tag) []html2html.Tag {
	attrSpecs := conv.ampValidatorRules.getAttrSpecs(tagSpec)

	hasMandatoryAttr := false
	for _, attrSpec := range attrSpecs {
		if attrSpec.GetMandatory() {
			hasMandatoryAttr = true
			break
		}
	}

	var resultList []html2html.Tag

	if hasMandatoryAttr {
		// mandatory有りの場合普通にチェックして引っかかったものを採用
		for _, tag := range rootTag.GetElementsByTagName(tagSpec.GetTagName()) {
			if !conv.isSpecMatchedTag(tagSpec, tag) {
				continue
			}

			resultList = append(resultList, tag)
		}
	} else {
		// mandatory無しの場合、タグが持つ全てのattrが規約に違反しなかったものを採用
		// そうしないと "meta name= and content=" に全部吸い込まれたりする
	outer:
		for _, tag := range rootTag.GetElementsByTagName(tagSpec.GetTagName()) {
			if !conv.isSpecMatchedTag(tagSpec, tag) {
				continue
			}

			for _, attr := range tag.Attrs() {
				found := false
				for _, attrSpec := range attrSpecs {
					if isAttrSpecMatch(attrSpec, attr) {
						found = true
						break
					}
				}
				if !found {
					continue outer
				}
			}

			resultList = append(resultList, tag)
		}
	}

	return resultList
}
func (conv *Converter) isSpecMatchedTag(tagSpec *amppb.TagSpec, tag html2html.Tag) bool {
	if htmlFormats := tagSpec.GetHtmlFormat(); len(htmlFormats) != 0 {
		found := false
		for _, htmlFormat := range htmlFormats {
			if htmlFormat == conv.ampValidatorRules.targetHTMLFormat {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	if tagSpec.GetTagName() == "!DOCTYPE" {
		// ignore at the here
		return false
	}
	if strings.ToLower(tagSpec.GetTagName()) != strings.ToLower(tag.Name()) {
		return false
	}

	if tagSpec.MandatoryParent != nil {
		parent := tag.Parent()
		if parent == nil {
			return false
		}

		if tagSpec.GetMandatoryParent() == "!DOCTYPE" && !parent.IsDocumentRoot() {
			// amp validator is $ROOT -child- !DOCTYPE -child- tag
			// but html2htmml is $ROOT -child- [!DOCTYPE, tag]
			return false
		} else if parent.IsDocumentRoot() {
			// ok
		} else if strings.ToLower(tagSpec.GetMandatoryParent()) != strings.ToLower(parent.Name()) {
			return false
		}
	}

	for _, attrSpec := range conv.ampValidatorRules.getAttrSpecs(tagSpec) {
		attr := tag.GetAttr(attrSpec.GetName())
		if attrSpec.GetMandatory() && attr == nil {
			for _, altName := range attrSpec.GetAlternativeNames() {
				if tag.HasAttr(altName) {
					attr = tag.GetAttr(altName)
					break
				}
			}

			if attr == nil {
				return false
			}
		}

		if attr == nil {
			continue
		}
		if !isAttrSpecMatch(attrSpec, attr) {
			return false
		}
	}

	return true
}

func (conv *Converter) willBeReuse(tag html2html.Tag) bool {
	return conv.ampValidatorRules.countTagSpecs(tag.Name()) == 1
}

func (conv *Converter) findReusableTag(tagSpec *amppb.TagSpec, rootTag html2html.Tag) html2html.Tag {
	matchTags := conv.specMatchedTags(tagSpec, rootTag)
	if len(matchTags) == 0 && tagSpec.GetMandatory() && tagSpec.GetUnique() && conv.ampValidatorRules.countTagSpecs(tagSpec.GetTagName()) == 1 {
		// mandatoryかつuniqかつSpecがHtmlFormat毎に1種類しかないタグの場合、既存のタグを再利用する
		targetTags := rootTag.GetElementsByTagName(tagSpec.GetTagName())
		if len(targetTags) != 0 {
			return targetTags[0]
		}
	}

	return nil
}

func (conv *Converter) tagSpecToToken(tagSpec *amppb.TagSpec) html2html.Token {
	if v := tagSpec.GetTagName(); v == "!DOCTYPE" {
		return html2html.CreateDoctypeToken("html")
	}

	var tag html2html.Tag
	if v := tagSpec.GetSpecName(); v == "head \u003e style[amp-boilerplate]" {
		style := html2html.CreateElement("style")
		style.AddAttr("amp-boilerplate", "")
		style.AddChildTokens(html2html.CreateTextToken(`body{-webkit-animation:-amp-start 8s steps(1,end) 0s 1 normal both;-moz-animation:-amp-start 8s steps(1,end) 0s 1 normal both;-ms-animation:-amp-start 8s steps(1,end) 0s 1 normal both;animation:-amp-start 8s steps(1,end) 0s 1 normal both}@-webkit-keyframes -amp-start{from{visibility:hidden}to{visibility:visible}}@-moz-keyframes -amp-start{from{visibility:hidden}to{visibility:visible}}@-ms-keyframes -amp-start{from{visibility:hidden}to{visibility:visible}}@-o-keyframes -amp-start{from{visibility:hidden}to{visibility:visible}}@keyframes -amp-start{from{visibility:hidden}to{visibility:visible}}`))
		tag = style

	} else if v == "noscript enclosure for boilerplate" {
		noscript := html2html.CreateElement("noscript")
		style := html2html.CreateElement("style")
		style.AddAttr("amp-boilerplate", "")
		noscript.AddChildTokens(style)
		style.AddChildTokens(html2html.CreateTextToken(`body{-webkit-animation:none;-moz-animation:none;-ms-animation:none;animation:none}`))
		tag = noscript

	} else {
		tag = html2html.CreateElement(strings.ToLower(tagSpec.GetTagName()))

		for _, attrSpec := range conv.ampValidatorRules.getAttrSpecs(tagSpec) {
			if !attrSpec.GetMandatory() {
				continue
			}

			if attrSpec.Value != nil {
				tag.AddAttr(attrSpec.GetName(), attrSpec.GetValue())
			} else if attrSpec.ValueCasei != nil {
				tag.AddAttr(attrSpec.GetName(), attrSpec.GetValueCasei())
			} else if attrSpec.ValueProperties != nil {
				var vs []string
				for _, propSpec := range attrSpec.GetValueProperties().GetProperties() {
					if !propSpec.GetMandatory() {
						continue
					}

					if propSpec.Value != nil {
						vs = append(vs, fmt.Sprintf("%s=%s", propSpec.GetName(), propSpec.GetValue()))
					} else if propSpec.ValueDouble != nil {
						vs = append(vs, fmt.Sprintf("%s=%g", propSpec.GetName(), propSpec.GetValueDouble()))
					} else {
						conv.addAMPError(&AMPError{
							Type:                AMPCreationTag,
							token:               tag,
							validatorSourceSpec: tagSpec,
							cause:               propSpec,
						})
					}
				}
				tag.AddAttr(attrSpec.GetName(), strings.Join(vs, ","))
			}
		}

		if tag.Name() == "link" && tag.HasAttrValue("rel", "canonical") {
			tag.AddAttr("href", conv.canonicalURL)
		}
	}

	if conv.debug && tagSpec.Cdata == nil && !html2html.IsVoidElement(tag) {
		desc := tagSpec.GetSpecName()
		if desc == "" {
			desc = tagSpec.GetTagName()
		}
		debugToken := html2html.CreateCommentToken(fmt.Sprintf("from: %s", desc))
		tag.AddChildTokens(debugToken)
	}

	return tag
}

func (conv *Converter) insertTagByTagSpec(rootTag html2html.Tag, tagSpec *amppb.TagSpec, tag html2html.Token) {
	if tagSpec.MandatoryParent != nil {
		parentTagName := tagSpec.GetMandatoryParent()

		if parentTagName == "$ROOT" || parentTagName == "!DOCTYPE" {
			rootTag.AddChildTokens(tag)
			return
		}

		tags := rootTag.GetElementsByTagName(parentTagName)
		if len(tags) == 0 {
			conv.addAMPError(&AMPError{
				Type:                AMPInsertinoTag,
				token:               tag,
				validatorSourceSpec: tagSpec,
				cause:               tagSpec,
			})
			return
		}
		tags[0].AddChildTokens(tag)
		return
	}

	tags := rootTag.GetElementsByTagName("body")
	if len(tags) == 0 {
		conv.addAMPError(&AMPError{
			Type:                AMPInsertinoTag,
			token:               tag,
			validatorSourceSpec: tagSpec,
			cause:               tagSpec,
		})
		return
	}
	tags[0].AddChildTokens(tag)
}

func parsePropertiesValue(attrValue string) map[string]string {
	valueMap := make(map[string]string)
	for _, kv := range strings.Split(attrValue, ",") {
		vs := strings.SplitN(kv, "=", 2)
		key := strings.TrimSpace(vs[0])
		value := ""
		if len(vs) == 2 {
			value = strings.TrimSpace(vs[1])
		}

		valueMap[key] = value
	}

	return valueMap
}

func isLinkStyleSheet(token html2html.Token) bool {
	if token.Type() != html2html.TypeTagToken {
		return false
	}

	tag := token.Tag()

	if tag.Name() != "link" {
		return false
	}

	relAttr := tag.GetAttr("rel")
	if relAttr == nil {
		return false
	}
	if relAttr.Value != "stylesheet" {
		return false
	}

	return true
}

func isEmbedStyleSheet(token html2html.Token) bool {
	if token.Type() != html2html.TypeTagToken {
		return false
	}

	tag := token.Tag()

	if tag.Name() != "style" {
		return false
	}

	return true
}

func isAMPBoilerplateStyleTag(token html2html.Token) bool {
	if token.Type() != html2html.TypeTagToken {
		return false
	}

	tag := token.Tag()
	if tag.Name() != "style" {
		return false
	}
	if !tag.HasAttr("amp-boilerplate") {
		return false
	}

	return true
}

func isAMPBoilerplateNoScriptTag(token html2html.Token) bool {
	if token.Type() != html2html.TypeTagToken {
		return false
	}

	tag := token.Tag()
	if tag.Name() != "noscript" {
		return false
	}
	for _, styleTag := range tag.GetElementsByTagName("style") {
		if isAMPBoilerplateStyleTag(styleTag) {
			return true
		}
	}

	return false
}

func isDefaultAMPScriptTag(token html2html.Token) bool {
	if token.Type() != html2html.TypeTagToken {
		return false
	}

	tag := token.Tag()
	if tag.Name() != "script" {
		return false
	}
	if !tag.HasAttr("async") {
		return false
	}
	if attr := tag.GetAttr("src"); attr != nil && attr.Value != "https://cdn.ampproject.org/v0.js" {
		return false
	}

	return true
}
