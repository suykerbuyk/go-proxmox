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

// TestCluster_UnmarshalJSON_Shape pins that every /cluster/status field of
// the wrong type is a *ShapeError, where the decoder used to panic, and
// that null or absent leaves the field unset.
func TestCluster_UnmarshalJSON_Shape(t *testing.T) {
	for _, c := range []struct {
		entry, field, want, got string
	}{
		{`{"type":7}`, "type", "string", "number"},
		{`{"type":"cluster","id":1}`, "id", "string", "number"},
		{`{"type":"cluster","name":false}`, "name", "string", "bool"},
		{`{"type":"cluster","version":"8"}`, "version", "number", "string"},
		{`{"type":"cluster","quorate":true}`, "quorate", "number", "bool"},
		{`{"type":"node","name":["n1"]}`, "name", "string", "array"},
		{`{"type":"node","level":{}}`, "level", "string", "object"},
		{`{"type":"node","online":"1"}`, "online", "number", "string"},
		{`{"type":"node","id":2}`, "id", "string", "number"},
		{`{"type":"node","ip":1}`, "ip", "string", "number"},
		{`{"type":"node","local":"0"}`, "local", "number", "string"},
	} {
		t.Run(c.entry, func(t *testing.T) {
			var cl Cluster
			var err error
			// A good entry first: the error names the bad one's index.
			require.NotPanics(t, func() { err = json.Unmarshal([]byte(`[{"type":"node","name":"n0"},`+c.entry+"]"), &cl) })
			requireShapeError(t, err, "Cluster", "[1]."+c.field, c.want, c.got)
		})
	}

	var cl Cluster
	require.NoError(t, json.Unmarshal([]byte(`[
		{"type":"cluster","id":"cluster","name":"lab","version":8,"quorate":1},
		{"type":"node","name":"n1","level":null,"online":1,"id":"node/n1","ip":null,"local":1},
		{"type":"node","name":"n2","online":0}
	]`), &cl))
	assert.Equal(t, Cluster{ID: "cluster", Name: "lab", Version: 8, Quorate: 1, Nodes: NodeStatuses{
		{Name: "n1", Type: "node", Status: "online", Online: 1, ID: "node/n1", Local: 1},
		{Name: "n2", Type: "node", Status: "offline"},
	}}, cl)
}

// TestCluster_UnmarshalJSON_EntryWithoutType: an entry with no type, or a
// null one, is a *ShapeError naming its index. It used to end the loop and
// silently drop every entry after it.
func TestCluster_UnmarshalJSON_EntryWithoutType(t *testing.T) {
	for raw, got := range map[string]string{
		`[{"type":"node","name":"n1"},{"name":"n2"},{"type":"node","name":"n3"}]`: "absent",
		`[{"type":"node","name":"n1"},{"type":null},{"type":"node","name":"n3"}]`: "null",
		`[{"type":"node","name":"n1"},null,{"type":"node","name":"n3"}]`:          "absent",
	} {
		var cl Cluster
		err := json.Unmarshal([]byte(raw), &cl)
		requireShapeError(t, err, "Cluster", "[1].type", "string", got)
	}
}

// TestLog_UnmarshalJSON_Shape: a log row's n or t of the wrong type is a
// *ShapeError, where the decoder used to panic; a row missing either, or
// holding null, is skipped as before.
func TestLog_UnmarshalJSON_Shape(t *testing.T) {
	for _, c := range []struct{ raw, field, want, got string }{
		{`[{"n":"1","t":"x"}]`, "n", "number", "string"},
		{`[{"n":1,"t":2}]`, "t", "string", "number"},
		{`[{"n":1,"t":["x"]}]`, "t", "string", "array"},
	} {
		var l Log
		var err error
		require.NotPanics(t, func() { err = json.Unmarshal([]byte(c.raw), &l) })
		requireShapeError(t, err, "Log", c.field, c.want, c.got)
	}
	var l Log
	require.NoError(t, json.Unmarshal([]byte(`[{"n":1,"t":"a"},{"n":2,"t":null},{"n":null,"t":"c"},{"t":"d"},{"n":4,"t":"e"}]`), &l))
	assert.Equal(t, Log{0: "a", 3: "e"}, l)
}

// TestLog_UnmarshalJSON_RowMissingAKeyIsSkipped: a row is type-checked only
// when it carries both n and t. A row with one of them, whatever its type,
// is skipped, as it always was; erroring on it would narrow what decoded.
func TestLog_UnmarshalJSON_RowMissingAKeyIsSkipped(t *testing.T) {
	for raw, want := range map[string]Log{
		`[{"t":5}]`:                            {},
		`[{"n":"1"}]`:                          {},
		`[{"n":1,"t":"a"},{"t":{}}]`:           {0: "a"},
		`[{"n":[1],"t":null},{"n":2,"t":"b"}]`: {1: "b"},
	} {
		var l Log
		require.NoError(t, json.Unmarshal([]byte(raw), &l), raw)
		assert.Equal(t, want, l, raw)
	}
}
