package tlsutil

import (
	"crypto/tls"
	"fmt"
)

// curveNameToID maps human-readable curve names to the tls.CurveID constant.
// Names are accepted in several common spellings.
var curveNameToID = map[string]tls.CurveID{
	"P-256":     tls.CurveP256,
	"P256":      tls.CurveP256,
	"CurveP256": tls.CurveP256,
	"P-384":     tls.CurveP384,
	"P384":      tls.CurveP384,
	"CurveP384": tls.CurveP384,
	"P-521":     tls.CurveP521,
	"P521":      tls.CurveP521,
	"CurveP521": tls.CurveP521,
	"X25519":    tls.X25519,
}

// GetCurveID returns the tls.CurveID corresponding to the given curve name,
// or an error if the name is not recognized.
func GetCurveID(name string) (tls.CurveID, error) {
	id, ok := curveNameToID[name]
	if !ok {
		return 0, fmt.Errorf("unrecognized TLS curve name %q", name)
	}
	return id, nil
}

// GetCurveIDs parses a slice of curve names into tls.CurveID values,
// preserving order.
func GetCurveIDs(names []string) ([]tls.CurveID, error) {
	ids := make([]tls.CurveID, 0, len(names))
	for _, n := range names {
		id, err := GetCurveID(n)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}
