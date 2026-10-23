package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/pflag"

	"github.com/jeffzi/ils/internal/landing"
	"github.com/jeffzi/ils/internal/result"
)

// refusedError carries a refusal up through Cobra, which only knows errors.
type refusedError struct {
	result result.Result
}

// Error returns a fixed text; the refusal itself travels in the result field.
func (e *refusedError) Error() string {
	return "the words were refused"
}

func (p *parse) refuse(reason result.Phrase) error {
	command := ""
	if len(p.words) > 0 {
		command = p.words[0]
	}

	req := landing.Request{Command: command, Words: p.words}

	return &refusedError{result: *req.Refused(reason, result.NextCommand(flagHelp))}
}

// extraWord is the reason command refuses word, which it was given besides its own.
func extraWord(command, word string) result.Phrase {
	return result.Say("%s takes no other word, and %s was given", command, word)
}

func notCommand(word string) result.Phrase {
	return result.Say("%s is not a command", word)
}

// flagRefusal turns a flag Cobra could not parse into a refusal: a value that is neither true
// nor false is named with its flag, and an unknown flag gets the reason unknown gives for the flag
// as it was typed. Any other error is returned as it is.
func (p *parse) flagRefusal(err error, unknown func(typed string) result.Phrase) error {
	if invalid, ok := errors.AsType[*pflag.InvalidValueError](err); ok {
		return p.refuse(result.Say("--%s takes true or false, and %s was given", invalid.GetFlag().Name, invalid.GetValue()))
	}

	missing, ok := errors.AsType[*pflag.NotExistError](err)
	if !ok {
		return err
	}

	// pflag names an unknown flag without its dashes; a group of short flags is named whole.
	typed := "--" + missing.GetSpecifiedName()
	if group := missing.GetSpecifiedShortnames(); group != "" {
		typed = "-" + group
	}

	return p.refuse(unknown(typed))
}

// strictBool is the value of a flag that takes no value but true and false.
type strictBool bool

// String returns "true" or "false".
func (b *strictBool) String() string {
	return fmt.Sprint(bool(*b))
}

// Set accepts only "true" and "false" and returns an error for any other value.
func (b *strictBool) Set(value string) error {
	switch value {
	case "true":
		*b = true
	case "false":
		*b = false
	default:
		return errors.New("neither true nor false")
	}

	return nil
}

// Type returns "bool" so pflag's help shows the flag without a value placeholder.
func (*strictBool) Type() string {
	return "bool"
}
