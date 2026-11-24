package repo

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestTransactionIdentityValueJSON(t *testing.T) {
	in := []TransactionIdentityValue{
		{Key: "rpc.grpc.status_code", Value: "0"},
		{Key: "http.response.status_code", Value: "403"},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if want := `[{"rpc.grpc.status_code":"0"},{"http.response.status_code":"403"}]`; string(data) != want {
		t.Fatalf("marshal = %s, want %s", data, want)
	}
	var out []TransactionIdentityValue
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(out, in) {
		t.Fatalf("unmarshal = %+v, want %+v", out, in)
	}
	if err := json.Unmarshal([]byte(`[{"a":"1","b":"2"}]`), &out); err == nil {
		t.Fatal("want an error for an object with two keys")
	}
}

func TestSplitOperationIdentity(t *testing.T) {
	name, dims := SplitOperationIdentity("POST /buy")
	if name != "POST /buy" || len(dims) != 0 {
		t.Fatalf("plain: name=%q dims=%v", name, dims)
	}

	name, dims = SplitOperationIdentity("POST /buy [http.response.status_code=200]")
	if name != "POST /buy" || len(dims) != 1 {
		t.Fatalf("one dim: name=%q dims=%v", name, dims)
	}
	if dims[0].Key != "http.response.status_code" || dims[0].Value != "200" {
		t.Fatalf("dim = %+v", dims[0])
	}

	op := "POST /com.jfrog.jfconnect.v1.entitlements.JFConnectResource/GetEntitlements [http.response.status_code=200]"
	name, dims = SplitOperationIdentity(op)
	if name != "POST /com.jfrog.jfconnect.v1.entitlements.JFConnectResource/GetEntitlements" {
		t.Fatalf("grpc name = %q", name)
	}
	if len(dims) != 1 || dims[0].Value != "200" {
		t.Fatalf("grpc dims = %v", dims)
	}

	op = "op [a=1][b=2]"
	name, dims = SplitOperationIdentity(op)
	if name != "op" || len(dims) != 2 {
		t.Fatalf("two dims: name=%q dims=%v", name, dims)
	}
	if dims[0].Key != "a" || dims[0].Value != "1" || dims[1].Key != "b" || dims[1].Value != "2" {
		t.Fatalf("dims = %+v", dims)
	}

	op = `op [weird=a\=b\]c]`
	name, dims = SplitOperationIdentity(op)
	if name != "op" || len(dims) != 1 || dims[0].Value != "a=b]c" {
		t.Fatalf("escaped: name=%q dims=%v", name, dims)
	}
}
