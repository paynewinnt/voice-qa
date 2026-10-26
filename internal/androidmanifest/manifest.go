package androidmanifest

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
)

const (
	resXMLType          = 0x0003
	resStringPoolType   = 0x0001
	resXMLStartElemType = 0x0102
	resXMLEndElemType   = 0x0103
	utf8Flag            = 0x00000100
	noStringIndex       = ^uint32(0)
	maxManifestSize     = 32 << 20
)

// Manifest contains the Activity declarations needed by the GUI's deep scan.
type Manifest struct {
	PackageName        string
	ApplicationEnabled bool
	Activities         []Activity
}

// Activity represents either an <activity> or <activity-alias> declaration.
type Activity struct {
	Name           string
	Alias          bool
	TargetActivity string
	Exported       bool
	ExportedSet    bool
	Enabled        bool
	EnabledSet     bool
	IntentFilters  []IntentFilter
}

// EffectiveExported applies Android's historical default for declarations that
// do not explicitly specify android:exported.
func (a Activity) EffectiveExported() bool {
	if a.ExportedSet {
		return a.Exported
	}
	return len(a.IntentFilters) > 0
}

// EffectiveEnabled combines the application and Activity enabled flags.
func (a Activity) EffectiveEnabled(applicationEnabled bool) bool {
	return applicationEnabled && (!a.EnabledSet || a.Enabled)
}

type IntentFilter struct {
	Actions    []string
	Categories []string
	Data       []DataSpec
}

type DataSpec struct {
	Scheme      string
	Host        string
	Port        string
	Path        string
	PathPrefix  string
	PathPattern string
	MIMEType    string
}

// ParseAPK reads the compiled AndroidManifest.xml from an APK.
func ParseAPK(path string) (Manifest, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("打开 APK: %w", err)
	}
	defer reader.Close()

	for _, file := range reader.File {
		if file.Name != "AndroidManifest.xml" {
			continue
		}
		if file.UncompressedSize64 > maxManifestSize {
			return Manifest{}, fmt.Errorf("AndroidManifest.xml 过大: %d bytes", file.UncompressedSize64)
		}
		stream, err := file.Open()
		if err != nil {
			return Manifest{}, fmt.Errorf("打开 AndroidManifest.xml: %w", err)
		}
		data, readErr := io.ReadAll(io.LimitReader(stream, maxManifestSize+1))
		closeErr := stream.Close()
		if readErr != nil {
			return Manifest{}, fmt.Errorf("读取 AndroidManifest.xml: %w", readErr)
		}
		if closeErr != nil {
			return Manifest{}, fmt.Errorf("关闭 AndroidManifest.xml: %w", closeErr)
		}
		if len(data) > maxManifestSize {
			return Manifest{}, fmt.Errorf("AndroidManifest.xml 超过 %d bytes", maxManifestSize)
		}
		return Parse(data)
	}
	return Manifest{}, fmt.Errorf("APK 中缺少 AndroidManifest.xml")
}

// Parse supports both compiled Android binary XML and plain XML fixtures.
func Parse(data []byte) (Manifest, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return Manifest{}, fmt.Errorf("AndroidManifest.xml 为空")
	}
	if trimmed[0] == '<' {
		return parsePlainXML(trimmed)
	}
	return parseBinaryXML(data)
}

type manifestBuilder struct {
	manifest      Manifest
	activityIndex int
	filterIndex   int
}

func newManifestBuilder() *manifestBuilder {
	return &manifestBuilder{
		manifest:      Manifest{ApplicationEnabled: true},
		activityIndex: -1,
		filterIndex:   -1,
	}
}

func (b *manifestBuilder) start(name string, attrs map[string]string) {
	switch name {
	case "manifest":
		b.manifest.PackageName = strings.TrimSpace(attrs["package"])
	case "application":
		if value, ok := attrs["enabled"]; ok {
			if enabled, valid := parseBool(value); valid {
				b.manifest.ApplicationEnabled = enabled
			}
		}
	case "activity", "activity-alias":
		activity := Activity{
			Name:           strings.TrimSpace(attrs["name"]),
			Alias:          name == "activity-alias",
			TargetActivity: strings.TrimSpace(attrs["targetActivity"]),
			Enabled:        true,
		}
		if value, ok := attrs["exported"]; ok {
			activity.Exported, activity.ExportedSet = parseBool(value)
		}
		if value, ok := attrs["enabled"]; ok {
			activity.Enabled, activity.EnabledSet = parseBool(value)
		}
		b.manifest.Activities = append(b.manifest.Activities, activity)
		b.activityIndex = len(b.manifest.Activities) - 1
		b.filterIndex = -1
	case "intent-filter":
		if b.activityIndex < 0 {
			return
		}
		activity := &b.manifest.Activities[b.activityIndex]
		activity.IntentFilters = append(activity.IntentFilters, IntentFilter{})
		b.filterIndex = len(activity.IntentFilters) - 1
	case "action":
		if filter := b.currentFilter(); filter != nil {
			filter.Actions = appendUnique(filter.Actions, strings.TrimSpace(attrs["name"]))
		}
	case "category":
		if filter := b.currentFilter(); filter != nil {
			filter.Categories = appendUnique(filter.Categories, strings.TrimSpace(attrs["name"]))
		}
	case "data":
		if filter := b.currentFilter(); filter != nil {
			filter.Data = append(filter.Data, DataSpec{
				Scheme:      strings.TrimSpace(attrs["scheme"]),
				Host:        strings.TrimSpace(attrs["host"]),
				Port:        strings.TrimSpace(attrs["port"]),
				Path:        strings.TrimSpace(attrs["path"]),
				PathPrefix:  strings.TrimSpace(attrs["pathPrefix"]),
				PathPattern: strings.TrimSpace(attrs["pathPattern"]),
				MIMEType:    strings.TrimSpace(attrs["mimeType"]),
			})
		}
	}
}

func (b *manifestBuilder) end(name string) {
	switch name {
	case "intent-filter":
		b.filterIndex = -1
	case "activity", "activity-alias":
		b.activityIndex = -1
		b.filterIndex = -1
	}
}

func (b *manifestBuilder) currentFilter() *IntentFilter {
	if b.activityIndex < 0 || b.activityIndex >= len(b.manifest.Activities) {
		return nil
	}
	activity := &b.manifest.Activities[b.activityIndex]
	if b.filterIndex < 0 || b.filterIndex >= len(activity.IntentFilters) {
		return nil
	}
	return &activity.IntentFilters[b.filterIndex]
}

func appendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func parseBool(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "0xffffffff":
		return true, true
	case "false", "0", "0x00000000":
		return false, true
	default:
		return false, false
	}
}

func parsePlainXML(data []byte) (Manifest, error) {
	builder := newManifestBuilder()
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Manifest{}, fmt.Errorf("解析文本 AndroidManifest.xml: %w", err)
		}
		switch node := token.(type) {
		case xml.StartElement:
			attrs := make(map[string]string, len(node.Attr))
			for _, attr := range node.Attr {
				attrs[attr.Name.Local] = attr.Value
			}
			builder.start(node.Name.Local, attrs)
		case xml.EndElement:
			builder.end(node.Name.Local)
		}
	}
	return validateManifest(builder.manifest)
}

func parseBinaryXML(data []byte) (Manifest, error) {
	if len(data) < 8 || uint16At(data, 0) != resXMLType {
		return Manifest{}, fmt.Errorf("不支持的 AndroidManifest.xml 格式")
	}
	documentSize := int(uint32At(data, 4))
	if documentSize < 8 || documentSize > len(data) {
		return Manifest{}, fmt.Errorf("AndroidManifest.xml 长度无效")
	}
	headerSize := int(uint16At(data, 2))
	if headerSize < 8 || headerSize > documentSize {
		return Manifest{}, fmt.Errorf("AndroidManifest.xml 头长度无效")
	}

	builder := newManifestBuilder()
	var pool stringPool
	for offset := headerSize; offset+8 <= documentSize; {
		chunkType := uint16At(data, offset)
		chunkHeaderSize := int(uint16At(data, offset+2))
		chunkSize := int(uint32At(data, offset+4))
		if chunkHeaderSize < 8 || chunkSize < chunkHeaderSize || offset+chunkSize > documentSize {
			return Manifest{}, fmt.Errorf("AndroidManifest.xml chunk 在偏移 %d 处损坏", offset)
		}

		switch chunkType {
		case resStringPoolType:
			parsed, err := parseStringPool(data[offset : offset+chunkSize])
			if err != nil {
				return Manifest{}, err
			}
			pool = parsed
		case resXMLStartElemType:
			name, attrs, err := parseStartElement(data[offset:offset+chunkSize], pool)
			if err != nil {
				return Manifest{}, err
			}
			builder.start(name, attrs)
		case resXMLEndElemType:
			name, err := parseEndElement(data[offset:offset+chunkSize], pool)
			if err != nil {
				return Manifest{}, err
			}
			builder.end(name)
		}
		offset += chunkSize
	}
	return validateManifest(builder.manifest)
}

func validateManifest(manifest Manifest) (Manifest, error) {
	if manifest.PackageName == "" {
		return Manifest{}, fmt.Errorf("AndroidManifest.xml 缺少 package")
	}
	activities := manifest.Activities[:0]
	for _, activity := range manifest.Activities {
		if activity.Name != "" {
			activities = append(activities, activity)
		}
	}
	manifest.Activities = activities
	return manifest, nil
}

type stringPool struct {
	values []string
}

func (p stringPool) get(index uint32) string {
	if index == noStringIndex || int(index) >= len(p.values) {
		return ""
	}
	return p.values[index]
}

func parseStringPool(chunk []byte) (stringPool, error) {
	if len(chunk) < 28 {
		return stringPool{}, fmt.Errorf("AndroidManifest.xml 字符串池过短")
	}
	headerSize := int(uint16At(chunk, 2))
	stringCount := int(uint32At(chunk, 8))
	flags := uint32At(chunk, 16)
	stringsStart := int(uint32At(chunk, 20))
	if headerSize < 28 || stringCount < 0 || headerSize+stringCount*4 > len(chunk) || stringsStart > len(chunk) {
		return stringPool{}, fmt.Errorf("AndroidManifest.xml 字符串池索引无效")
	}

	pool := stringPool{values: make([]string, stringCount)}
	for index := 0; index < stringCount; index++ {
		stringOffset := int(uint32At(chunk, headerSize+index*4))
		absolute := stringsStart + stringOffset
		if absolute < stringsStart || absolute >= len(chunk) {
			return stringPool{}, fmt.Errorf("AndroidManifest.xml 字符串偏移无效")
		}
		var value string
		var err error
		if flags&utf8Flag != 0 {
			value, err = decodeUTF8String(chunk, absolute)
		} else {
			value, err = decodeUTF16String(chunk, absolute)
		}
		if err != nil {
			return stringPool{}, err
		}
		pool.values[index] = value
	}
	return pool, nil
}

func decodeUTF8String(data []byte, offset int) (string, error) {
	_, consumed, err := decodeLength8(data, offset)
	if err != nil {
		return "", err
	}
	offset += consumed
	byteLength, consumed, err := decodeLength8(data, offset)
	if err != nil {
		return "", err
	}
	offset += consumed
	if byteLength < 0 || offset+byteLength > len(data) {
		return "", fmt.Errorf("AndroidManifest.xml UTF-8 字符串越界")
	}
	return string(data[offset : offset+byteLength]), nil
}

func decodeLength8(data []byte, offset int) (int, int, error) {
	if offset >= len(data) {
		return 0, 0, fmt.Errorf("AndroidManifest.xml UTF-8 长度越界")
	}
	first := data[offset]
	if first&0x80 == 0 {
		return int(first), 1, nil
	}
	if offset+1 >= len(data) {
		return 0, 0, fmt.Errorf("AndroidManifest.xml UTF-8 双字节长度越界")
	}
	return int(first&0x7f)<<8 | int(data[offset+1]), 2, nil
}

func decodeUTF16String(data []byte, offset int) (string, error) {
	length, consumed, err := decodeLength16(data, offset)
	if err != nil {
		return "", err
	}
	offset += consumed
	if length < 0 || offset+length*2 > len(data) {
		return "", fmt.Errorf("AndroidManifest.xml UTF-16 字符串越界")
	}
	units := make([]uint16, length)
	for index := range units {
		units[index] = uint16At(data, offset+index*2)
	}
	return string(utf16.Decode(units)), nil
}

func decodeLength16(data []byte, offset int) (int, int, error) {
	if offset+2 > len(data) {
		return 0, 0, fmt.Errorf("AndroidManifest.xml UTF-16 长度越界")
	}
	first := uint16At(data, offset)
	if first&0x8000 == 0 {
		return int(first), 2, nil
	}
	if offset+4 > len(data) {
		return 0, 0, fmt.Errorf("AndroidManifest.xml UTF-16 双字长度越界")
	}
	return int(first&0x7fff)<<16 | int(uint16At(data, offset+2)), 4, nil
}

func parseStartElement(chunk []byte, pool stringPool) (string, map[string]string, error) {
	if len(chunk) < 36 {
		return "", nil, fmt.Errorf("AndroidManifest.xml 开始节点过短")
	}
	name := pool.get(uint32At(chunk, 20))
	attributeStart := int(uint16At(chunk, 24))
	attributeSize := int(uint16At(chunk, 26))
	attributeCount := int(uint16At(chunk, 28))
	if name == "" || attributeSize < 20 {
		return "", nil, fmt.Errorf("AndroidManifest.xml 开始节点无效")
	}
	firstAttribute := 16 + attributeStart
	if firstAttribute < 36 || firstAttribute+attributeSize*attributeCount > len(chunk) {
		return "", nil, fmt.Errorf("AndroidManifest.xml 属性列表越界")
	}

	attrs := make(map[string]string, attributeCount)
	for index := 0; index < attributeCount; index++ {
		offset := firstAttribute + index*attributeSize
		attrName := pool.get(uint32At(chunk, offset+4))
		if attrName == "" {
			continue
		}
		rawIndex := uint32At(chunk, offset+8)
		value := pool.get(rawIndex)
		if rawIndex == noStringIndex {
			value = decodeTypedValue(chunk[offset+12:offset+20], pool)
		}
		attrs[attrName] = value
	}
	return name, attrs, nil
}

func parseEndElement(chunk []byte, pool stringPool) (string, error) {
	if len(chunk) < 24 {
		return "", fmt.Errorf("AndroidManifest.xml 结束节点过短")
	}
	name := pool.get(uint32At(chunk, 20))
	if name == "" {
		return "", fmt.Errorf("AndroidManifest.xml 结束节点无效")
	}
	return name, nil
}

func decodeTypedValue(value []byte, pool stringPool) string {
	if len(value) < 8 {
		return ""
	}
	dataType := value[3]
	data := binary.LittleEndian.Uint32(value[4:8])
	switch dataType {
	case 0x03: // TYPE_STRING
		return pool.get(data)
	case 0x10: // TYPE_INT_DEC
		return strconv.FormatInt(int64(int32(data)), 10)
	case 0x11: // TYPE_INT_HEX
		return fmt.Sprintf("0x%08x", data)
	case 0x12: // TYPE_INT_BOOLEAN
		if data != 0 {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("0x%08x", data)
	}
}

func uint16At(data []byte, offset int) uint16 {
	return binary.LittleEndian.Uint16(data[offset : offset+2])
}

func uint32At(data []byte, offset int) uint32 {
	return binary.LittleEndian.Uint32(data[offset : offset+4])
}
