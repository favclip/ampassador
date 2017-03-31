package amppb

import (
	"io/ioutil"

	"github.com/golang/protobuf/proto"
)

func ParseRules(textFormat string) (*ValidatorRules, error) {
	if textFormat == "" {
		content, err := ioutil.ReadFile("./validator-main.protoascii")
		if err != nil {
			return nil, err
		}
		textFormat = string(content)
	}

	rules := &ValidatorRules{}
	err := proto.UnmarshalText(textFormat, rules)
	if err != nil {
		return nil, err
	}

	return rules, err
}
