package data

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

func TestSubjectPrefixFilterAnchorsAndEscapesThePrefix(t *testing.T) {
	f := subjectPrefixFilter("auth0|a.b+c/")
	re := f[0].Value.(bson.D)[0].Value.(string)
	if re != `^auth0\|a\.b\+c/` {
		t.Errorf("regex = %q", re)
	}
}
