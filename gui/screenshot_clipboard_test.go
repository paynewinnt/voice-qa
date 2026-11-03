package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestScreenshotClipboardDIBLayout(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	img.SetNRGBA(1, 0, color.NRGBA{G: 255, A: 255})
	img.SetNRGBA(0, 1, color.NRGBA{B: 255, A: 255})
	img.SetNRGBA(1, 1, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	dib, err := screenshotClipboardDIB(encoded.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(dib) != 56 || binary.LittleEndian.Uint32(dib[:4]) != 40 || binary.LittleEndian.Uint32(dib[4:8]) != 2 || binary.LittleEndian.Uint32(dib[8:12]) != 2 || binary.LittleEndian.Uint16(dib[12:14]) != 1 || binary.LittleEndian.Uint16(dib[14:16]) != 24 || binary.LittleEndian.Uint32(dib[20:24]) != 16 {
		t.Fatalf("incorrect DIB header: %v", dib)
	}
	want := []byte{255, 0, 0, 255, 255, 255, 0, 0, 0, 0, 255, 0, 255, 0, 0, 0}
	if !bytes.Equal(dib[40:], want) {
		t.Fatalf("wrong color order, orientation or padding: %v", dib[40:])
	}
}

func TestScreenshotClipboardDIBTransparencyAndInvalidData(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 128})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	dib, err := screenshotClipboardDIB(encoded.Bytes())
	if err != nil || !bytes.Equal(dib[40:], []byte{127, 127, 255, 0}) {
		t.Fatalf("incorrect alpha flattening: %v %v", dib, err)
	}
	for _, data := range [][]byte{nil, []byte("not PNG"), encoded.Bytes()[:encoded.Len()-12]} {
		if _, err := screenshotClipboardDIB(data); err == nil {
			t.Fatal("invalid PNG accepted")
		}
	}
}
