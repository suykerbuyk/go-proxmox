package proxmox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/h2non/gock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShapeError(t *testing.T) {
	var err error = &ShapeError{Type: "Task", Field: "starttime", Want: "number", Got: "string"}
	assert.EqualError(t, err, `proxmox: decode Task: field "starttime" is string, want number`)
	assert.True(t, errors.Is(fmt.Errorf("wrapped: %w", err), ErrUnexpectedShape))
}

// requireShapeError asserts err is a *ShapeError for typ.field, got kind.
func requireShapeError(t *testing.T, err error, typ, field, want, got string) {
	t.Helper()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnexpectedShape), "errors.Is(%v, ErrUnexpectedShape)", err)
	var se *ShapeError
	require.True(t, errors.As(err, &se), "errors.As(%v, *ShapeError)", err)
	assert.Equal(t, ShapeError{Type: typ, Field: field, Want: want, Got: got}, *se)
}

// TestTask_UnmarshalJSON_Times pins that a task time of the wrong type is a
// *ShapeError, where the decoder used to panic asserting float64, and that a
// null or absent time leaves it zero (a running task has no endtime).
func TestTask_UnmarshalJSON_Times(t *testing.T) {
	for _, field := range []string{"starttime", "endtime"} {
		for _, bad := range []struct{ raw, kind string }{
			{`"1727000000"`, "string"}, {`true`, "bool"}, {`{}`, "object"}, {`[1]`, "array"},
		} {
			t.Run(field+"="+bad.raw, func(t *testing.T) {
				var task Task
				var err error
				require.NotPanics(t, func() {
					err = json.Unmarshal([]byte(fmt.Sprintf(`{"status":"stopped",%q:%s}`, field, bad.raw)), &task)
				})
				requireShapeError(t, err, "Task", field, "number", bad.kind)
			})
		}
		t.Run(field+"=null", func(t *testing.T) {
			var task Task
			require.NoError(t, json.Unmarshal([]byte(fmt.Sprintf(`{"status":"running",%q:null}`, field)), &task))
			assert.True(t, task.StartTime.IsZero())
			assert.True(t, task.EndTime.IsZero())
			assert.Equal(t, "running", task.Status)
		})
	}
	var task Task
	require.NoError(t, json.Unmarshal([]byte(`{"status":"stopped","starttime":1727000000,"endtime":1727000060}`), &task))
	assert.Equal(t, time.Unix(1727000000, 0), task.StartTime)
	assert.Equal(t, time.Unix(1727000060, 0), task.EndTime)
	assert.Equal(t, time.Minute, task.Duration)
}

// TestTask_Ping_ShapeError: a status poll whose starttime is not a number
// returns the *ShapeError from Ping, where it used to panic.
func TestTask_Ping_ShapeError(t *testing.T) {
	defer gock.Off()
	const upid = "UPID:node1:00000001:00000001:00000001:test:shape:root@pam:"
	gock.New(TestURI).
		Get("^/nodes/node1/tasks/" + upid + "/status$").
		Reply(200).
		JSON(`{"data":{"upid":"` + upid + `","node":"node1","status":"stopped","exitstatus":"OK","starttime":"x"}}`)
	task := NewTask(UPID(upid), NewClient(TestURI))
	var err error
	require.NotPanics(t, func() { err = task.Ping(context.Background()) })
	requireShapeError(t, err, "Task", "starttime", "number", "string")
	assert.True(t, gock.IsDone())
}

func TestShapeFieldHelpers(t *testing.T) {
	m := map[string]interface{}{"n": 1.5, "s": "x", "null": nil, "wrong": "1"}
	n, ok, err := numberField("T", m, "n")
	assert.Equal(t, 1.5, n)
	assert.True(t, ok)
	assert.NoError(t, err)
	for _, key := range []string{"null", "absent"} {
		_, ok, err = numberField("T", m, key)
		assert.False(t, ok, key)
		assert.NoError(t, err, key)
		_, ok, err = stringField("T", m, key)
		assert.False(t, ok, key)
		assert.NoError(t, err, key)
	}
	_, _, err = numberField("T", m, "wrong")
	requireShapeError(t, err, "T", "wrong", "number", "string")
	s, ok, err := stringField("T", m, "s")
	assert.Equal(t, "x", s)
	assert.True(t, ok)
	assert.NoError(t, err)
	_, _, err = stringField("T", m, "n")
	requireShapeError(t, err, "T", "n", "string", "number")
}
