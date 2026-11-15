package main

import (
	"reflect"
	"testing"
)

func TestCopyBlocksOrderAndExactContent(t *testing.T) {
	a := copyBlocks("-- header\nCOPY public.users (id) FROM stdin;\n2\n1\n\\.\nCOPY public.tickets (id) FROM stdin;\n\\.\n")
	b := copyBlocks("COPY public.users (id) FROM stdin;\n1\n2\n\\.\nCOPY public.tickets (id) FROM stdin;\n\\.\n")
	if len(a) != 2 || !reflect.DeepEqual(a, b) {
		t.Fatal("row order must not affect restore comparison")
	}
	b["COPY public.users (id) FROM stdin;"][0] = "modified"
	if reflect.DeepEqual(a, b) {
		t.Fatal("changed data must fail")
	}
}
