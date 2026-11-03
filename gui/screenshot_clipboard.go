package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image/png"
)

// CF_DIB uses a BITMAPINFOHEADER followed by bottom-up, four-byte-aligned BGR rows.
func screenshotClipboardDIB(data []byte) ([]byte, error) {
	info, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if info.Width > 16384 || info.Height > 16384 || int64(info.Width)*int64(info.Height) > 64*1024*1024 {
		return nil, fmt.Errorf("截图尺寸异常")
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	stride := (info.Width*3 + 3) &^ 3
	dib := make([]byte, 40+stride*info.Height)
	binary.LittleEndian.PutUint32(dib[0:4], 40)
	binary.LittleEndian.PutUint32(dib[4:8], uint32(info.Width))
	binary.LittleEndian.PutUint32(dib[8:12], uint32(info.Height))
	binary.LittleEndian.PutUint16(dib[12:14], 1)
	binary.LittleEndian.PutUint16(dib[14:16], 24)
	binary.LittleEndian.PutUint32(dib[20:24], uint32(stride*info.Height))
	for y := 0; y < info.Height; y++ {
		row := 40 + (info.Height-1-y)*stride
		for x := 0; x < info.Width; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			// Flatten transparency on white for applications that only accept CF_DIB.
			dib[row+x*3] = byte((b + 0xffff - a) >> 8)
			dib[row+x*3+1] = byte((g + 0xffff - a) >> 8)
			dib[row+x*3+2] = byte((r + 0xffff - a) >> 8)
		}
	}
	return dib, nil
}
