package ldap

import (
	"fmt"
	"strings"
)

// The result codes of RFC 4511 appendix A that this client acts on.
const (
	ResultSuccess                 = 0
	ResultOperationsError         = 1
	ResultProtocolError           = 2
	ResultTimeLimitExceeded       = 3
	ResultSizeLimitExceeded       = 4
	ResultAuthMethodNotSupported  = 7
	ResultStrongerAuthRequired    = 8
	ResultReferral                = 10
	ResultConfidentialityRequired = 13
	ResultNoSuchObject            = 32
	ResultInvalidDNSyntax         = 34
	ResultInvalidCredentials      = 49
	ResultInsufficientAccess      = 50
	ResultBusy                    = 51
	ResultUnavailable             = 52
	ResultUnwillingToPerform      = 53
)

// resultNames are the codes an operator might see, named.
//
// A bare number sends somebody to a search engine; "49 invalid
// credentials" ends the question.
var resultNames = map[int]string{
	ResultSuccess:                 "success",
	ResultOperationsError:         "operations error",
	ResultProtocolError:           "protocol error",
	ResultTimeLimitExceeded:       "time limit exceeded",
	ResultSizeLimitExceeded:       "size limit exceeded",
	ResultAuthMethodNotSupported:  "auth method not supported",
	ResultStrongerAuthRequired:    "stronger auth required",
	ResultReferral:                "referral, which this client does not chase",
	ResultConfidentialityRequired: "confidentiality required",
	ResultNoSuchObject:            "no such object",
	ResultInvalidDNSyntax:         "invalid DN syntax",
	ResultInvalidCredentials:      "invalid credentials",
	ResultInsufficientAccess:      "insufficient access rights",
	ResultBusy:                    "busy",
	ResultUnavailable:             "unavailable",
	ResultUnwillingToPerform:      "unwilling to perform",
}

// Result is an LDAPResult that was not success.
type Result struct {
	Code      int
	MatchedDN string
	Message   string
}

func (r *Result) Error() string {
	name, ok := resultNames[r.Code]
	if !ok {
		name = "result code " + fmt.Sprint(r.Code)
	}
	var b strings.Builder
	b.WriteString(name)
	if r.Message != "" {
		b.WriteString(": " + strings.TrimSpace(r.Message))
	}
	return b.String()
}

func (r *Result) String() string { return r.Error() }

// IsInvalidCredentials reports the one failure a login must tell apart
// from every other: the operator got their password wrong, which is
// their problem, against the directory being unreachable or refusing
// this client, which is the estate's.
func IsInvalidCredentials(err error) bool {
	var result *Result
	if !asResult(err, &result) {
		return false
	}
	return result.Code == ResultInvalidCredentials
}

func asResult(err error, out **Result) bool {
	for err != nil {
		if r, ok := err.(*Result); ok {
			*out = r
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// resultOf reads an LDAPResult out of a protocol operation.
func resultOf(op element) (*Result, error) {
	children, err := op.children()
	if err != nil {
		return nil, err
	}
	if len(children) < 3 {
		return nil, fmt.Errorf("an LDAP result with %d parts", len(children))
	}
	code, err := children[0].number()
	if err != nil {
		return nil, err
	}
	return &Result{
		Code:      code,
		MatchedDN: children[1].text(),
		Message:   children[2].text(),
	}, nil
}

// entryOf reads a SearchResultEntry.
func entryOf(op element) (Entry, error) {
	children, err := op.children()
	if err != nil {
		return Entry{}, err
	}
	if len(children) < 2 {
		return Entry{}, fmt.Errorf("a search entry with %d parts", len(children))
	}
	entry := Entry{DN: children[0].text(), Attributes: map[string][]string{}}

	attributes, err := children[1].children()
	if err != nil {
		return Entry{}, err
	}
	for _, attribute := range attributes {
		parts, err := attribute.children()
		if err != nil {
			return Entry{}, err
		}
		if len(parts) < 2 {
			continue
		}
		name := parts[0].text()
		values, err := parts[1].children()
		if err != nil {
			return Entry{}, err
		}
		list := make([]string, 0, len(values))
		for _, v := range values {
			list = append(list, v.text())
		}
		entry.Attributes[name] = list
	}
	return entry, nil
}

// equalFold compares attribute names, which LDAP treats as
// case-insensitive and which Active Directory and OpenLDAP spell
// differently.
func equalFold(a, b string) bool { return strings.EqualFold(a, b) }

// ErrMalformedRequest is a login this client refused before asking the
// directory anything — an empty password, most often.
var ErrMalformedRequest = fmt.Errorf("the login was not well formed")

// Classify names why a login failed, for the log.
//
// A boolean "was the directory reachable" gets this wrong in the case
// that happens most: somebody submits the form with the password field
// blank, which this client refuses without asking the directory, and
// which is not an outage. An estate alerting on outages would be
// alerted every time.
func Classify(err error) string {
	switch {
	case err == nil:
		return "accepted"
	case IsInvalidCredentials(err):
		return "invalid_credentials"
	case errorIs(err, ErrNoSuchUser):
		return "no_such_user"
	case errorIs(err, ErrMalformedRequest):
		return "malformed_request"
	}
	var result *Result
	if asResult(err, &result) {
		// The directory answered, and said something other than
		// "wrong password" — insufficient access, unwilling to
		// perform, a referral. The estate's problem, not the
		// operator's, but not an outage either.
		return "directory_refused"
	}
	return "directory_unreachable"
}

func errorIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}
