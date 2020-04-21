package util

import (
	"encoding/hex"
	"regexp"
	"strings"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"

	"bytes"
	"encoding/gob"

	"crypto/md5"
	"fmt"

	"github.com/blang/semver"
	"go.uber.org/zap"
)

// ************** This is a file containing any general "algorithmic" or "util" functions used by other packages

// FindLeftDifference finds the difference between arrays of string - the elements that are present in "left" but absent
// in "right" array
func FindLeftDifference(left, right []string) []string {
	ans := make([]string, 0)
	for _, v := range left {
		if !stringutil.Contains(right, v) {
			ans = append(ans, v)
		}
	}
	return ans
}

// Int32Ref is required to return a *int32, which can't be declared as a literal.
func Int32Ref(i int32) *int32 {
	return &i
}

// Int64Ref is required to return a *int64, which can't be declared as a literal.
func Int64Ref(i int64) *int64 {
	return &i
}

// Float64Ref is required to return a *float64, which can't be declared as a literal.
func Float64Ref(i float64) *float64 {
	return &i
}

// BooleanRef is required to return a *bool, which can't be declared as a literal.
func BooleanRef(b bool) *bool {
	return &b
}

func StripEnt(version string) string {
	return strings.Trim(version, "-ent")
}

// DoAndRetry performs the task 'f' until it returns true or 'count' retrials are executed. Sleeps for 'interval' seconds
// between retries. String return parameter contains the fail message that is printed in case of failure.
func DoAndRetry(f func() (string, bool), log *zap.SugaredLogger, count, interval int) bool {
	for i := 0; i < count; i++ {
		msg, ok := f()
		if ok {
			return true
		}
		if msg != "" {
			msg += "."
		}
		log.Debugf("%s Retrying %d/%d (waiting for %d more seconds)", msg, i+1, count, interval)
		time.Sleep(time.Duration(interval) * time.Second)
	}
	return false
}

// MapDeepCopy is a quick implementation of deep copy mechanism for any Go structures, it uses Go serialization and
// deserialization mechanisms so will always be slower than any manual copy
// https://rosettacode.org/wiki/Deepcopy#Go
func MapDeepCopy(m map[string]interface{}) (map[string]interface{}, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	dec := gob.NewDecoder(&buf)
	err := enc.Encode(m)
	if err != nil {
		return nil, err
	}
	var copy map[string]interface{}
	err = dec.Decode(&copy)
	if err != nil {
		return nil, err
	}
	return copy, nil
}

func ReadOrCreateMap(m map[string]interface{}, key string) map[string]interface{} {
	if _, ok := m[key]; !ok {
		m[key] = make(map[string]interface{})
	}
	return m[key].(map[string]interface{})
}

func ReadOrCreateStringArray(m map[string]interface{}, key string) []string {
	if _, ok := m[key]; !ok {
		m[key] = make([]string, 0)
	}
	return m[key].([]string)
}

func CompareVersions(version1, version2 string) (int, error) {
	v1, err := semver.Make(version1)
	if err != nil {
		return 0, err
	}
	v2, err := semver.Make(version2)
	if err != nil {
		return 0, err
	}
	return v1.Compare(v2), nil
}

func VersionMatchesRange(version, vRange string) (bool, error) {
	v, err := semver.Parse(version)
	if err != nil {
		return false, err
	}
	expectedRange, err := semver.ParseRange(vRange)
	if err != nil {
		return false, err
	}
	return expectedRange(v), nil
}

func MajorMinorVersion(version string) (string, error) {
	v1, err := semver.Make(version)
	if err != nil {
		return "", nil
	}
	return fmt.Sprintf("%d.%d", v1.Major, v1.Minor), nil
}

// ************ Different functions to work with environment variables **************

func MaxInt(x, y int) int {
	if x > y {
		return x
	}
	return y
}

// ************ Different string/array functions **************
//
// Helper functions to check and remove string from a slice of strings.
//

// MD5Hex computes the MDB checksum of the given string as per https://golang.org/pkg/crypto/md5/
func MD5Hex(s string) string {
	h := md5.New()
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))
}

// RedactMongoURI will strip the password out of the MongoURI and replace it with the text "<redacted>"
//
func RedactMongoURI(uri string) string {
	if !strings.Contains(uri, "@") {
		return uri
	}
	re := regexp.MustCompile("(mongodb://.*:)(.*)(@.*:.*)")
	return re.ReplaceAllString(uri, "$1<redacted>$3")
}

func Redact(toRedact interface{}) string {
	if toRedact == nil {
		return "nil"
	}
	return "<redacted>"
}
