package query

import (
	"context"
	"errors"
	"fmt"
)

// ErrUnknownForm is returned for a form name that iq does not know.
var ErrUnknownForm = errors.New("unknown record form")

// Form names the dialect of a Record value. A form names a dialect, not a driver,
// so every driver that shares a dialect reads and writes the same form.
type Form int

const (
	// FormJSON is the normalized JSON that every read path produces. It is the
	// zero value, so a record that sets no form is a normalized one.
	FormJSON Form = iota
	// FormDynamoJSON is one DynamoDB JSON item, for example {"price": {"N": "1.50"}}.
	FormDynamoJSON
	// FormMongoEJSON is one canonical Extended JSON document, for example
	// {"n": {"$numberInt": "7"}}. No driver reads or writes it yet.
	FormMongoEJSON
)

// formNames maps each form to its name in a typed dump, in the order of the
// constants. A form without a name does not exist.
var formNames = [...]string{
	FormJSON:       "json",
	FormDynamoJSON: "dynamodb-json",
	FormMongoEJSON: "mongo-ejson",
}

// String returns the name of f as a typed dump spells it. An out-of-range form
// gets a name that marks it as unknown.
func (f Form) String() string {
	if f < 0 || int(f) >= len(formNames) {
		return fmt.Sprintf("form(%d)", int(f))
	}
	return formNames[f]
}

// ParseForm returns the form that a typed dump names. An empty name is FormJSON,
// because a dump omits the name of the default form. Any other unknown name is
// an error, so a dump from a newer version of iq is refused and not misread.
func ParseForm(name string) (Form, error) {
	if name == "" {
		return FormJSON, nil
	}
	for f, n := range formNames {
		if n == name {
			return Form(f), nil
		}
	}
	return FormJSON, fmt.Errorf("%w %q", ErrUnknownForm, name)
}

// DumpName returns the value of the "form" field of a typed dump for f. It is
// empty for FormJSON, so a dump of normalized records is the same as before.
func (f Form) DumpName() string {
	if f == FormJSON {
		return ""
	}
	return f.String()
}

// NativeReader is an optional capability of a store: it reads each item in the
// native form of its driver. The records carry that form in Record.Form, and
// Value holds the decoded typed JSON. A store without this port refuses a
// native read. fn gets one page at a time, and the call stops at the first
// error from fn or the store. ctx bounds the call.
type NativeReader interface {
	NativeScan(ctx context.Context, fn func(batch []Record) error) error
	// NativeForm names the form of the records that NativeScan emits. A caller
	// reads it before the scan, to refuse a destination that cannot write it.
	NativeForm() Form
}

// FormWriter is an optional capability of a Putter: it names the native form that
// the Putter writes, besides FormJSON.
type FormWriter interface {
	WritesForm() Form
}

// WriterForm returns the native form that p writes. It is FormJSON when p has no
// native form, which means p writes only normalized JSON.
func WriterForm(p Putter) Form {
	if fw, ok := p.(FormWriter); ok {
		return fw.WritesForm()
	}
	return FormJSON
}

// CheckForms makes sure that a Putter can write every record of batch. A Putter
// writes FormJSON and the one native form that it owns. The check refuses any
// other record with an error that names the driver, the key, and the forms. A
// driver with no native form passes FormJSON as own.
func CheckForms(driver string, own Form, batch []Record) error {
	for _, r := range batch {
		if r.Form == FormJSON || r.Form == own {
			continue
		}
		if own == FormJSON {
			return fmt.Errorf("%s: record %q has form %q, but this destination accepts only form %q",
				driver, r.Key, r.Form, FormJSON)
		}
		return fmt.Errorf("%s: record %q has form %q, but this destination accepts only forms %q and %q",
			driver, r.Key, r.Form, FormJSON, own)
	}
	return nil
}
