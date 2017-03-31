package amphtml

import (
	"bytes"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/ioutil"
	"net/url"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/favclip/html2html"
)

func TestConverter_ConvertToFullHTML(t *testing.T) {

	dirs, err := ioutil.ReadDir("./fixture")
	if err != nil {
		t.Fatal(err)
	}

	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}

		files, err := ioutil.ReadDir("./fixture/" + dir.Name())
		if err != nil {
			t.Fatal(err)
		}

		for _, file := range files {
			if !strings.HasSuffix(file.Name(), ".html") && !strings.HasSuffix(file.Name(), ".htm") {
				continue
			}

			fileName := "./fixture/" + dir.Name() + "/" + file.Name()

			t.Log("target", fileName)

			f, err := os.Open(fileName)
			if err != nil {
				t.Fatal(fileName, err)
			}
			defer f.Close()

			tag, err := html2html.NewConverter().Parse(f)
			if err != nil {
				t.Fatal(fileName, err)
			}

			cURLOpt := WithCanonicalURL("https://example.com/foo/bar")
			ffOpt := WithFileFetcher(func(targetURL *url.URL) (io.ReadCloser, error) {
				requestedFile := path.Join("./fixture/"+dir.Name(), targetURL.Path)
				return os.Open(requestedFile)
			})
			conv, err := NewConverter(cURLOpt, ffOpt)
			if err != nil {
				t.Fatal(fileName, err)
			}
			tag, err = conv.ConvertToFullHTML(tag)
			if ampErrors, ok := err.(AMPErrors); ok && len(ampErrors) != 0 {
				if ampErrors.HasFatalError() {
					t.Fatal(ampErrors)
				}
			} else if err != nil {
				t.Fatal(fileName, err)
			}

			buf := bytes.NewBufferString("")
			tag.BuildHTML(buf)

			expectedfileName := "./expected/" + dir.Name() + "/" + file.Name()

			var expected []byte
			if _, err = os.Stat(expectedfileName); os.IsNotExist(err) {
				expected = buf.Bytes()
				err = os.MkdirAll("./expected/"+dir.Name(), os.ModePerm)
				if err != nil {
					t.Fatal(expectedfileName, err)
				}
				err = ioutil.WriteFile(expectedfileName, expected, os.ModePerm)
				if err != nil {
					t.Fatal(expectedfileName, err)
				}
			} else {
				expected, err = ioutil.ReadFile(expectedfileName)
				if err != nil {
					t.Fatal(expectedfileName, err)
				}
			}

			if actual := buf.String(); actual != string(expected) {
				t.Error(fileName, "unexpected", actual)
			}
		}
	}
}
