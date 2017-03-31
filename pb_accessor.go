package amphtml

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/favclip/ampassador/amppb"
	"github.com/favclip/html2html"
)

type wrappedRules struct {
	targetHTMLFormat amppb.TagSpec_HtmlFormat
	rules            *amppb.ValidatorRules
}

type attrSpecEx amppb.AttrSpec

func newWrappedRules(text string) (*wrappedRules, error) {
	rules, err := amppb.ParseRules(text)
	if err != nil {
		return nil, err
	}

	w := &wrappedRules{
		targetHTMLFormat: amppb.TagSpec_AMP,
		rules:            rules,
	}

	return w, nil
}

func (w *wrappedRules) countTagSpecs(tagName string) int {
	tagName = strings.ToLower(tagName)

	count := 0
	for _, tagSpec := range w.rules.GetTags() {
		if htmlFormats := tagSpec.GetHtmlFormat(); len(htmlFormats) != 0 {
			found := false
			for _, htmlFormat := range htmlFormats {
				if htmlFormat == w.targetHTMLFormat {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		if strings.ToLower(tagSpec.GetTagName()) == tagName {
			count++
		}
	}

	return count
}

func (w *wrappedRules) getAttrSpecs(tagSpec *amppb.TagSpec) []*amppb.AttrSpec {
	var resultList []*amppb.AttrSpec
	resultList = append(resultList, tagSpec.GetAttrs()...)
	for _, attrListName := range tagSpec.GetAttrLists() {
		resultList = append(resultList, w.findAttrList(attrListName).GetAttrs()...)
	}
	resultList = append(resultList, w.findAttrList("$GLOBAL_ATTRS").GetAttrs()...)

	return resultList
}

func (w *wrappedRules) findAttrList(attrListName string) *amppb.AttrList {
	for _, attrList := range w.rules.GetAttrLists() {
		if attrList.GetName() == attrListName {
			return attrList
		}
	}

	return nil
}

func isAttrSpecMatch(attrSpec *amppb.AttrSpec, attr *html2html.Attr) bool {
	if attr == nil {
		return false
	}

	if attrSpec.GetName() != attr.Key {
		return false
	}

	if attrSpec.Value != nil && attr.Value != attrSpec.GetValue() {
		return false
	}
	if attrSpec.ValueCasei != nil && strings.ToLower(attr.Value) != attrSpec.GetValueCasei() {
		return false
	}
	if attrSpec.ValueRegex != nil {
		re, err := regexp.Compile(attrSpec.GetValueRegex())
		if err != nil {
			panic(err)
		}

		if !re.MatchString(attr.Value) {
			return false
		}
	}
	if attrSpec.ValueRegexCasei != nil {
		re, err := regexp.Compile("(?i)" + attrSpec.GetValueRegexCasei())
		if err != nil {
			panic(err)
		}
		if !re.MatchString(attr.Value) {
			return false
		}
	}
	if attrSpec.BlacklistedValueRegex != nil {
		re, err := regexp.Compile("(?i)" + attrSpec.GetBlacklistedValueRegex())
		if err != nil {
			panic(err)
		}
		if !re.MatchString(attr.Value) {
			return false
		}
	}

	if attrSpec.ValueProperties != nil {
		valueMap := parsePropertiesValue(attr.Value)
		for _, propSpec := range attrSpec.GetValueProperties().GetProperties() {
			if v, ok := valueMap[propSpec.GetName()]; ok {
				if propSpec.Value != nil && propSpec.GetValue() != v {
					return false
				}
				if propSpec.ValueDouble != nil {
					v, err := strconv.ParseFloat(attr.Value, 64)
					if err != nil {
						return false
					}
					if propSpec.GetValueDouble() != v {
						return false
					}
				}
			} else if propSpec.GetMandatory() {
				return false
			}
		}
	}

	return true
}
