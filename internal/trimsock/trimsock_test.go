package trimsock

import (
	"reflect"
	"strings"
	"testing"
)

func all(t *testing.T, r *Reader, in ...string) []*Command {
	var out []*Command
	for _, s := range in {
		r.Ingest([]byte(s))
		for {
			c, ok := r.Next()
			if !ok {
				break
			}
			out = append(out, c)
		}
	}
	return out
}

func TestSimpleCommands(t *testing.T) {
	var r Reader
	cmds := all(t, &r, "register-host\nconnect abc\nconnect-re", "lay  x y\n")
	if len(cmds) != 3 {
		t.Fatalf("got %d", len(cmds))
	}
	if cmds[0].Name != "register-host" || cmds[0].Text != "" || !cmds[0].HasText {
		t.Errorf("%+v", cmds[0])
	}
	if cmds[1].Name != "connect" || cmds[1].Text != "abc" || cmds[1].Params != nil {
		t.Errorf("%+v", cmds[1])
	}
	// only one separator space is consumed; params need two or more items
	if cmds[2].Name != "connect-relay" || cmds[2].Text != " x y" || !reflect.DeepEqual(cmds[2].Params, []string{"x", "y"}) {
		t.Errorf("%+v", cmds[2])
	}
}

func TestCRLFIsNotStripped(t *testing.T) {
	var r Reader
	c := all(t, &r, "register-host\r\n")[0]
	if c.Name != "register-host\r" {
		t.Fatalf("%q", c.Name)
	}
}

func TestQuotesAndEscapes(t *testing.T) {
	var r Reader
	cmds := all(t, &r, "say \"hello world\" \\\"x\\n\nmulti \"a\nb\"\n")
	if cmds[0].Name != "say" || cmds[0].Text != "hello world \"x\n" {
		t.Errorf("%q", cmds[0].Text)
	}
	if cmds[1].Text != "a\nb" {
		t.Errorf("newline inside quotes: %q", cmds[1].Text)
	}
}

func TestConventions(t *testing.T) {
	var r Reader
	cmds := all(t, &r, "connect?r1 oid\nconnect.r1 ok\nconnect!r1\ndata|s1 chunk\ndata|s1\nkv a=b c\n")
	if c := cmds[0]; c.Name != "connect" || !c.IsRequest || c.RequestID != "r1" || c.Text != "oid" {
		t.Errorf("request %+v", c)
	}
	if c := cmds[1]; !c.IsSuccessResponse || c.Name != "connect" {
		t.Errorf("success %+v", c)
	}
	if c := cmds[2]; !c.IsErrorResponse {
		t.Errorf("error %+v", c)
	}
	if c := cmds[3]; !c.IsStreamChunk || c.StreamID != "s1" || c.Name != "data" {
		t.Errorf("chunk %+v", c)
	}
	if c := cmds[4]; !c.IsStreamEnd {
		t.Errorf("end %+v", c)
	}
	if c := cmds[5]; !reflect.DeepEqual(c.Params, []string{"c"}) {
		t.Errorf("kv %+v", c)
	}
	if id, ok := cmds[0].ID(); !ok || id != "r1" {
		t.Error("id")
	}
	if _, ok := (&Command{Name: "x"}).ID(); ok {
		t.Error("simple commands have no id")
	}
}

func TestRawCommand(t *testing.T) {
	var r Reader
	cmds := all(t, &r, "\rblob 5\nab", "cde\nnext\n")
	if len(cmds) != 2 || cmds[0].Name != "blob" || string(cmds[0].Raw) != "abcde" || cmds[0].HasText {
		t.Fatalf("%+v", cmds)
	}
	if cmds[1].Name != "next" {
		t.Fatalf("%+v", cmds[1])
	}
}

func TestOverflowResets(t *testing.T) {
	var r Reader
	if r.Ingest([]byte(strings.Repeat("x", MaxSize+1))) {
		t.Fatal("should overflow")
	}
	if cmds := all(t, &r, "register-host\n"); len(cmds) != 1 {
		t.Fatal("reader should recover")
	}
}

func TestEscapedQuoteInsideQuotes(t *testing.T) {
	var r Reader
	cmds := all(t, &r, "a \"x\\\"\nb\"\nok\n")
	if len(cmds) != 2 || cmds[0].Text != "x\"\nb" || cmds[1].Name != "ok" {
		t.Fatalf("%+v", cmds)
	}
}

func TestSerialize(t *testing.T) {
	cases := map[string]string{
		string(AppendCommand(nil, "set-oid", "abc")):       "set-oid abc\n",
		string(AppendCommand(nil, "connect", "1.2.3.4:5")): "connect 1.2.3.4:5\n",
		string(AppendCommand(nil, "connect")):              "connect\n",
		string(AppendCommand(nil, "x", "a b", "q\"")):      "x \"a b\" q\\\"\n",
		string(AppendCommand(nil, "x", "")):                "x\n",
		string(AppendErrorResponse(nil, "r1")):             "!r1\n",
		string(AppendCommand([]byte("a\n"), "b", "c")):     "a\nb c\n",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
}
