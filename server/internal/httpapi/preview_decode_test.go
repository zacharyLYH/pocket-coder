package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
)

func TestDecodePointShapes(t *testing.T) {
	x, y, err := decodePoint(json.RawMessage(`{"result":{"value":{"x":10.7,"y":20.2}}}`), ".x")
	if err != nil || x != 10 || y != 20 {
		t.Fatalf("got (%d,%d,%v), want (10,20,nil)", x, y, err)
	}
	if _, _, err := decodePoint(json.RawMessage(`{"result":{"value":null}}`), ".missing"); !errors.Is(err, errSelectorMiss) {
		t.Fatalf("null value err = %v, want errSelectorMiss", err)
	}
	if _, _, err := decodePoint(json.RawMessage(`{"result":{}}`), ".missing"); !errors.Is(err, errSelectorMiss) {
		t.Fatalf("missing value err = %v, want errSelectorMiss", err)
	}
	if _, _, err := decodePoint(json.RawMessage(`not json`), ".x"); err == nil {
		t.Fatal("garbage input must error")
	}
}

func TestDecodeEvaluateString(t *testing.T) {
	s, err := decodeEvaluateString(json.RawMessage(`{"result":{"value":"<html>"}}`))
	if err != nil || s != "<html>" {
		t.Fatalf("got (%q,%v), want (\"<html>\",nil)", s, err)
	}
	if _, err := decodeEvaluateString(json.RawMessage(`{"result":{"value":null}}`)); err == nil {
		t.Fatal("null value must error instead of an empty 200 payload")
	}
	if _, err := decodeEvaluateString(json.RawMessage(`{"error":{"message":"nope"}}`)); err == nil {
		t.Fatal("CDP error frame must error instead of an empty 200 payload")
	}
}

func TestDecodeScreenshotPNG(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString([]byte{1, 2, 3})})
	png, err := decodeScreenshotPNG(raw)
	if err != nil || len(png) != 3 {
		t.Fatalf("got (%v,%v), want 3 bytes", png, err)
	}
	if _, err := decodeScreenshotPNG(json.RawMessage(`{"data":""}`)); err == nil {
		t.Fatal("empty data must error")
	}
	if _, err := decodeScreenshotPNG(json.RawMessage(`{"data":"!!!"}`)); err == nil {
		t.Fatal("bad base64 must error")
	}
}
