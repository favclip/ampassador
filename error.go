package amphtml

import (
	"bytes"
	"fmt"

	"github.com/favclip/ampassador/amppb"
	"github.com/favclip/html2html"
)

var _ error = &AMPError{}

type AMPErrorType int

const (
	AMPValidatorError AMPErrorType = iota + 1
	AMPValidatorWarning
	AMPRemoveAttr
	AMPDeprecation
	AMPCreationTag
	AMPInsertinoTag
)

func (v AMPErrorType) String() string {
	switch v {
	case AMPValidatorError:
		return "AMPValidatorError"
	case AMPValidatorWarning:
		return "AMPValidatorWarning"
	case AMPRemoveAttr:
		return "AMPRemoveAttr"
	case AMPDeprecation:
		return "AMPDeprecation"
	case AMPCreationTag:
		return "AMPCreationTag"
	case AMPInsertinoTag:
		return "AMPInsertinoTag"
	}

	return "unknown"
}

type AMPError struct {
	Type AMPErrorType

	token               html2html.Token
	validatorSourceSpec *amppb.TagSpec
	cause               interface{}
}

func (e *AMPError) Error() string {
	switch e.Type {
	case AMPValidatorError, AMPCreationTag, AMPInsertinoTag:
		return fmt.Sprintf("err %s spec: %s", e.Type, e.validatorSourceSpec.GetSpecName())
	case AMPValidatorWarning, AMPRemoveAttr, AMPDeprecation:
		return fmt.Sprintf("warn %s spec: %s", e.Type, e.validatorSourceSpec.GetSpecName())
	}

	return "AMPError: undenifed"
}

type AMPErrors []*AMPError

func (e AMPErrors) Error() string {
	errBuf := bytes.NewBufferString("")
	warnBuf := bytes.NewBufferString("")

	for _, ampErr := range e {
		switch ampErr.Type {
		case AMPValidatorError, AMPCreationTag, AMPInsertinoTag:
			errBuf.WriteString(ampErr.Error())
		case AMPValidatorWarning, AMPRemoveAttr, AMPDeprecation:
			warnBuf.WriteString(ampErr.Error())
		}
	}

	return "AMPErrors: \n" + errBuf.String() + warnBuf.String()
}

func (e AMPErrors) HasFatalError() bool {
	for _, ampErr := range e {
		switch ampErr.Type {
		case AMPValidatorError, AMPCreationTag, AMPInsertinoTag:
			return true
		}
	}

	return false
}
