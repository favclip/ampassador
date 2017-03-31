package amphtml

import (
	"image"
	"io"
	"net/url"
	"strconv"

	"github.com/favclip/html2html"
)

var AMPTags AMPTagList

type AMPTagList []*AMPTag

type AMPTag struct {
	SrcTag   string
	DestTag  string
	Modifier TagModifier
}

type TagModifier func(conv *Converter, ampTag *AMPTag, tag html2html.Tag) (html2html.Tag, error)

type FileFetcher func(targetURL *url.URL) (io.ReadCloser, error)

func (list AMPTagList) isAMPTag(tag html2html.Tag) bool {
	for _, ampTag := range list {
		if tag.Name() == ampTag.DestTag {
			return true
		}
	}
	return false
}

type AMPImageStatsFetcher interface {
	ImageSize(imageURL *url.URL) (*url.URL, int, int, error) // modifiedURL, width, height
	ImageSrcSetAttr(imageURL *url.URL) (string, error)       // return value uses for <amp-img src=... srcset="{{Here!}}">
}

func init() {
	AMPTags = append(AMPTags, &AMPTag{SrcTag: "img", DestTag: "amp-img", Modifier: ampImageModifier})
	// TODO
	// video
	// audio
	// iframe
}

type ampImageStatsFetcherImpl struct {
	fileFetcher FileFetcher
}

func (a *ampImageStatsFetcherImpl) ImageSize(imageURL *url.URL) (*url.URL, int, int, error) {
	data, err := a.fileFetcher(imageURL)
	if err != nil {
		return imageURL, 0, 0, err
	}
	defer data.Close()

	config, _, err := image.DecodeConfig(data)
	if err != nil {
		return imageURL, 0, 0, err
	}

	return imageURL, config.Width, config.Height, nil
}

func (m *ampImageStatsFetcherImpl) ImageSrcSetAttr(imageURL *url.URL) (string, error) {
	return "", nil
}

func ampImageModifier(conv *Converter, ampTag *AMPTag, tag html2html.Tag) (html2html.Tag, error) {
	altTag := html2html.CreateElement(ampTag.DestTag)

	for _, attr := range tag.Attrs() {
		switch attr.Key {
		case "alt":
			altTag.AddAttr(attr.Key, attr.Value)
		case "src":
			imgURL, err := url.Parse(attr.Value)
			if err != nil {
				altTag.AddAttr(attr.Key, attr.Value)
				continue
			}

			imgURL, width, height, err := conv.ampImageStatsFetcher.ImageSize(imgURL)
			if err != nil {
				return nil, err
			}

			altTag.AddAttr("src", imgURL.String())

			altTag.AddAttr("width", strconv.Itoa(width))
			altTag.AddAttr("height", strconv.Itoa(height))
			altTag.AddAttr("layout", "responsive")

			srcset, err := conv.ampImageStatsFetcher.ImageSrcSetAttr(imgURL)
			if err != nil {
				return nil, err
			}

			if srcset != "" {
				altTag.AddAttr("srcset", srcset)
			}

		default:
			// ignore
		}
	}

	return altTag, nil
}
