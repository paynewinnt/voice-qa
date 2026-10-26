package androidmanifest

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestParsePlainManifestActivities(t *testing.T) {
	data := []byte(`<?xml version="1.0" encoding="utf-8"?>
<manifest xmlns:android="http://schemas.android.com/apk/res/android" package="com.example.app">
  <application android:enabled="true">
    <activity android:name=".SplashActivity" android:exported="true">
      <intent-filter>
        <action android:name="android.intent.action.VIEW" />
        <category android:name="android.intent.category.BROWSABLE" />
        <data android:scheme="aiquestion" android:host="open_page" />
      </intent-filter>
    </activity>
    <activity android:name=".MainActivity" android:exported="false" android:enabled="false" />
    <activity-alias android:name=".Launcher" android:targetActivity=".SplashActivity">
      <intent-filter><action android:name="android.intent.action.MAIN" /></intent-filter>
    </activity-alias>
  </application>
</manifest>`)

	manifest, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if manifest.PackageName != "com.example.app" || len(manifest.Activities) != 3 {
		t.Fatalf("manifest = %+v", manifest)
	}
	splash := manifest.Activities[0]
	if !splash.EffectiveExported() || !splash.EffectiveEnabled(manifest.ApplicationEnabled) {
		t.Fatalf("splash flags = %+v", splash)
	}
	if len(splash.IntentFilters) != 1 || splash.IntentFilters[0].Data[0].Scheme != "aiquestion" {
		t.Fatalf("splash filters = %+v", splash.IntentFilters)
	}
	main := manifest.Activities[1]
	if main.EffectiveExported() || main.EffectiveEnabled(manifest.ApplicationEnabled) {
		t.Fatalf("main flags = %+v", main)
	}
	alias := manifest.Activities[2]
	if !alias.Alias || alias.TargetActivity != ".SplashActivity" || !alias.EffectiveExported() {
		t.Fatalf("alias = %+v", alias)
	}
}

func TestParseBinaryManifestFromAPK(t *testing.T) {
	manifestData := buildBinaryManifest(t)
	apkPath := filepath.Join(t.TempDir(), "sample.apk")
	file, err := os.Create(apkPath)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	entry, err := archive.Create("AndroidManifest.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(manifestData); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	manifest, err := ParseAPK(apkPath)
	if err != nil {
		t.Fatalf("ParseAPK() error = %v", err)
	}
	if manifest.PackageName != "com.example.binary" || len(manifest.Activities) != 2 {
		t.Fatalf("manifest = %+v", manifest)
	}
	if got := manifest.Activities[0]; got.Name != ".SplashActivity" || !got.EffectiveExported() || len(got.IntentFilters) != 1 {
		t.Fatalf("first activity = %+v", got)
	}
	if got := manifest.Activities[1]; got.Name != ".MainActivity" || got.EffectiveExported() {
		t.Fatalf("second activity = %+v", got)
	}
}

func TestParseAPKFromEnvironment(t *testing.T) {
	path := os.Getenv("VOICE_QA_MANIFEST_TEST_APK")
	if path == "" {
		t.Skip("set VOICE_QA_MANIFEST_TEST_APK for a real-device APK smoke test")
	}
	manifest, err := ParseAPK(path)
	if err != nil {
		t.Fatalf("ParseAPK(%q) error = %v", path, err)
	}
	if manifest.PackageName == "" || len(manifest.Activities) == 0 {
		t.Fatalf("manifest has no package or Activities: %+v", manifest)
	}
	t.Logf("parsed package %s with %d Activities", manifest.PackageName, len(manifest.Activities))
}

type binaryAttribute struct {
	name  string
	value string
}

func buildBinaryManifest(t *testing.T) []byte {
	t.Helper()
	strings := []string{
		"manifest", "package", "com.example.binary", "application", "activity", "name",
		".SplashActivity", "exported", "true", "intent-filter", "action",
		"android.intent.action.VIEW", "category", "android.intent.category.BROWSABLE",
		"data", "scheme", "aiquestion", "host", "open_page", ".MainActivity", "false",
	}
	indices := make(map[string]uint32, len(strings))
	for index, value := range strings {
		indices[value] = uint32(index)
	}

	chunks := [][]byte{
		binaryStringPool(strings),
		binaryStartElement(indices, "manifest", []binaryAttribute{{"package", "com.example.binary"}}),
		binaryStartElement(indices, "application", nil),
		binaryStartElement(indices, "activity", []binaryAttribute{{"name", ".SplashActivity"}, {"exported", "true"}}),
		binaryStartElement(indices, "intent-filter", nil),
		binaryStartElement(indices, "action", []binaryAttribute{{"name", "android.intent.action.VIEW"}}),
		binaryEndElement(indices, "action"),
		binaryStartElement(indices, "category", []binaryAttribute{{"name", "android.intent.category.BROWSABLE"}}),
		binaryEndElement(indices, "category"),
		binaryStartElement(indices, "data", []binaryAttribute{{"scheme", "aiquestion"}, {"host", "open_page"}}),
		binaryEndElement(indices, "data"),
		binaryEndElement(indices, "intent-filter"),
		binaryEndElement(indices, "activity"),
		binaryStartElement(indices, "activity", []binaryAttribute{{"name", ".MainActivity"}, {"exported", "false"}}),
		binaryEndElement(indices, "activity"),
		binaryEndElement(indices, "application"),
		binaryEndElement(indices, "manifest"),
	}

	size := 8
	for _, chunk := range chunks {
		size += len(chunk)
	}
	document := make([]byte, 8, size)
	binary.LittleEndian.PutUint16(document[0:2], resXMLType)
	binary.LittleEndian.PutUint16(document[2:4], 8)
	binary.LittleEndian.PutUint32(document[4:8], uint32(size))
	for _, chunk := range chunks {
		document = append(document, chunk...)
	}
	return document
}

func binaryStringPool(values []string) []byte {
	var encoded bytes.Buffer
	offsets := make([]uint32, len(values))
	for index, value := range values {
		offsets[index] = uint32(encoded.Len())
		encoded.WriteByte(byte(len([]rune(value))))
		encoded.WriteByte(byte(len(value)))
		encoded.WriteString(value)
		encoded.WriteByte(0)
	}
	for encoded.Len()%4 != 0 {
		encoded.WriteByte(0)
	}

	headerSize := 28
	stringsStart := headerSize + len(offsets)*4
	size := stringsStart + encoded.Len()
	chunk := make([]byte, size)
	binary.LittleEndian.PutUint16(chunk[0:2], resStringPoolType)
	binary.LittleEndian.PutUint16(chunk[2:4], uint16(headerSize))
	binary.LittleEndian.PutUint32(chunk[4:8], uint32(size))
	binary.LittleEndian.PutUint32(chunk[8:12], uint32(len(values)))
	binary.LittleEndian.PutUint32(chunk[16:20], utf8Flag)
	binary.LittleEndian.PutUint32(chunk[20:24], uint32(stringsStart))
	for index, offset := range offsets {
		binary.LittleEndian.PutUint32(chunk[headerSize+index*4:], offset)
	}
	copy(chunk[stringsStart:], encoded.Bytes())
	return chunk
}

func binaryStartElement(indices map[string]uint32, name string, attrs []binaryAttribute) []byte {
	size := 36 + len(attrs)*20
	chunk := make([]byte, size)
	binary.LittleEndian.PutUint16(chunk[0:2], resXMLStartElemType)
	binary.LittleEndian.PutUint16(chunk[2:4], 16)
	binary.LittleEndian.PutUint32(chunk[4:8], uint32(size))
	binary.LittleEndian.PutUint32(chunk[12:16], noStringIndex)
	binary.LittleEndian.PutUint32(chunk[16:20], noStringIndex)
	binary.LittleEndian.PutUint32(chunk[20:24], indices[name])
	binary.LittleEndian.PutUint16(chunk[24:26], 20)
	binary.LittleEndian.PutUint16(chunk[26:28], 20)
	binary.LittleEndian.PutUint16(chunk[28:30], uint16(len(attrs)))
	for index, attr := range attrs {
		offset := 36 + index*20
		binary.LittleEndian.PutUint32(chunk[offset:offset+4], noStringIndex)
		binary.LittleEndian.PutUint32(chunk[offset+4:offset+8], indices[attr.name])
		binary.LittleEndian.PutUint32(chunk[offset+8:offset+12], indices[attr.value])
		binary.LittleEndian.PutUint16(chunk[offset+12:offset+14], 8)
		chunk[offset+15] = 0x03
		binary.LittleEndian.PutUint32(chunk[offset+16:offset+20], indices[attr.value])
	}
	return chunk
}

func binaryEndElement(indices map[string]uint32, name string) []byte {
	chunk := make([]byte, 24)
	binary.LittleEndian.PutUint16(chunk[0:2], resXMLEndElemType)
	binary.LittleEndian.PutUint16(chunk[2:4], 16)
	binary.LittleEndian.PutUint32(chunk[4:8], uint32(len(chunk)))
	binary.LittleEndian.PutUint32(chunk[12:16], noStringIndex)
	binary.LittleEndian.PutUint32(chunk[16:20], noStringIndex)
	binary.LittleEndian.PutUint32(chunk[20:24], indices[name])
	return chunk
}
