package dispositions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// These tests came from a codebase that used testify. Redline takes no test
// dependency, so assert and require here are the few checks those tests call,
// with the same names and argument order: assert reports and carries on,
// require stops the test.
var (
	assert  = checker{}
	require = checker{fatal: true}
)

type checker struct{ fatal bool }

func (c checker) fail(t testing.TB, msgAndArgs []any, format string, args ...any) bool {
	t.Helper()
	msg := fmt.Sprintf(format, args...)
	if len(msgAndArgs) > 0 {
		if f, ok := msgAndArgs[0].(string); ok {
			msg += ": " + fmt.Sprintf(f, msgAndArgs[1:]...)
		} else {
			msg += ": " + fmt.Sprint(msgAndArgs...)
		}
	}
	if c.fatal {
		t.Fatal(msg)
	}
	t.Error(msg)
	return false
}

func equal(want, got any) bool {
	if w, ok := want.([]byte); ok {
		g, ok := got.([]byte)
		return ok && bytes.Equal(w, g)
	}
	return reflect.DeepEqual(want, got)
}

func isEmpty(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Map, reflect.Chan, reflect.String, reflect.Array:
		return rv.Len() == 0
	case reflect.Pointer:
		return rv.IsNil() || isEmpty(rv.Elem().Interface())
	default:
		return rv.IsZero()
	}
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Chan, reflect.Func, reflect.Interface:
		return rv.IsNil()
	}
	return false
}

func contains(container, item any) bool {
	if s, ok := container.(string); ok {
		sub, ok := item.(string)
		return ok && strings.Contains(s, sub)
	}
	rv := reflect.ValueOf(container)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		for i := range rv.Len() {
			if equal(rv.Index(i).Interface(), item) {
				return true
			}
		}
	case reflect.Map:
		for _, k := range rv.MapKeys() {
			if equal(k.Interface(), item) {
				return true
			}
		}
	}
	return false
}

func (c checker) Equal(t testing.TB, want, got any, msgAndArgs ...any) bool {
	t.Helper()
	if !equal(want, got) {
		return c.fail(t, msgAndArgs, "not equal:\n  want: %#v\n  got:  %#v", want, got)
	}
	return true
}

func (c checker) NotEqual(t testing.TB, notWant, got any, msgAndArgs ...any) bool {
	t.Helper()
	if equal(notWant, got) {
		return c.fail(t, msgAndArgs, "should differ from %#v", got)
	}
	return true
}

func (c checker) Empty(t testing.TB, v any, msgAndArgs ...any) bool {
	t.Helper()
	if !isEmpty(v) {
		return c.fail(t, msgAndArgs, "should be empty, got %#v", v)
	}
	return true
}

func (c checker) NotEmpty(t testing.TB, v any, msgAndArgs ...any) bool {
	t.Helper()
	if isEmpty(v) {
		return c.fail(t, msgAndArgs, "should not be empty")
	}
	return true
}

func (c checker) Zero(t testing.TB, v any, msgAndArgs ...any) bool {
	t.Helper()
	if v != nil && !reflect.ValueOf(v).IsZero() {
		return c.fail(t, msgAndArgs, "should be zero, got %#v", v)
	}
	return true
}

func (c checker) Nil(t testing.TB, v any, msgAndArgs ...any) bool {
	t.Helper()
	if !isNil(v) {
		return c.fail(t, msgAndArgs, "should be nil, got %#v", v)
	}
	return true
}

func (c checker) NotNil(t testing.TB, v any, msgAndArgs ...any) bool {
	t.Helper()
	if isNil(v) {
		return c.fail(t, msgAndArgs, "should not be nil")
	}
	return true
}

func (c checker) True(t testing.TB, v bool, msgAndArgs ...any) bool {
	t.Helper()
	if !v {
		return c.fail(t, msgAndArgs, "should be true")
	}
	return true
}

func (c checker) False(t testing.TB, v bool, msgAndArgs ...any) bool {
	t.Helper()
	if v {
		return c.fail(t, msgAndArgs, "should be false")
	}
	return true
}

func (c checker) Len(t testing.TB, v any, n int, msgAndArgs ...any) bool {
	t.Helper()
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Map, reflect.Chan, reflect.String, reflect.Array:
		if rv.Len() != n {
			return c.fail(t, msgAndArgs, "should have %d item(s), has %d: %#v", n, rv.Len(), v)
		}
		return true
	}
	return c.fail(t, msgAndArgs, "cannot take the length of %#v", v)
}

func (c checker) Contains(t testing.TB, container, item any, msgAndArgs ...any) bool {
	t.Helper()
	if !contains(container, item) {
		return c.fail(t, msgAndArgs, "%#v does not contain %#v", container, item)
	}
	return true
}

func (c checker) NoError(t testing.TB, err error, msgAndArgs ...any) bool {
	t.Helper()
	if err != nil {
		return c.fail(t, msgAndArgs, "unexpected error: %v", err)
	}
	return true
}

func (c checker) Error(t testing.TB, err error, msgAndArgs ...any) bool {
	t.Helper()
	if err == nil {
		return c.fail(t, msgAndArgs, "expected an error")
	}
	return true
}

func (c checker) ErrorContains(t testing.TB, err error, sub string, msgAndArgs ...any) bool {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), sub) {
		return c.fail(t, msgAndArgs, "error %v should contain %q", err, sub)
	}
	return true
}

func (c checker) ErrorIs(t testing.TB, err, target error, msgAndArgs ...any) bool {
	t.Helper()
	if !errors.Is(err, target) {
		return c.fail(t, msgAndArgs, "error %v is not %v", err, target)
	}
	return true
}

func (c checker) JSONEq(t testing.TB, want, got string, msgAndArgs ...any) bool {
	t.Helper()
	var w, g any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		return c.fail(t, msgAndArgs, "want is not JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		return c.fail(t, msgAndArgs, "got is not JSON: %v", err)
	}
	if !reflect.DeepEqual(w, g) {
		return c.fail(t, msgAndArgs, "JSON differs:\n  want: %s\n  got:  %s", want, got)
	}
	return true
}

func (c checker) Fail(t testing.TB, msg string, msgAndArgs ...any) bool {
	t.Helper()
	return c.fail(t, msgAndArgs, "%s", msg)
}
