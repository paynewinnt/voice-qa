package audio

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// WAV 文件头结构
type WAVHeader struct {
	ChunkID       [4]byte // "RIFF"
	ChunkSize     uint32
	Format        [4]byte // "WAVE"
	Subchunk1ID   [4]byte // "fmt "
	Subchunk1Size uint32
	AudioFormat   uint16
	NumChannels   uint16
	SampleRate    uint32
	ByteRate      uint32
	BlockAlign    uint16
	BitsPerSample uint16
	Subchunk2ID   [4]byte // "data"
	Subchunk2Size uint32
}

// ReadWAV 读取 WAV 文件，返回头信息和音频数据
// 支持带有额外 chunks 的 WAV 文件（如 LIST、fact 等）
func ReadWAV(filepath string) (*WAVHeader, []byte, error) {
	data, err := os.ReadFile(filepath)
	if err != nil {
		return nil, nil, err
	}

	if len(data) < 44 {
		return nil, nil, fmt.Errorf("WAV 文件太小")
	}

	// 验证 RIFF 头
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, nil, fmt.Errorf("无效的 WAV 文件格式")
	}

	header := &WAVHeader{}
	copy(header.ChunkID[:], data[0:4])
	header.ChunkSize = binary.LittleEndian.Uint32(data[4:8])
	copy(header.Format[:], data[8:12])

	// 查找 fmt 和 data chunks
	offset := 12
	for offset < len(data)-8 {
		chunkID := string(data[offset : offset+4])
		chunkSize := binary.LittleEndian.Uint32(data[offset+4 : offset+8])

		if chunkID == "fmt " {
			copy(header.Subchunk1ID[:], data[offset:offset+4])
			header.Subchunk1Size = chunkSize
			if offset+8+int(chunkSize) <= len(data) && chunkSize >= 16 {
				header.AudioFormat = binary.LittleEndian.Uint16(data[offset+8 : offset+10])
				header.NumChannels = binary.LittleEndian.Uint16(data[offset+10 : offset+12])
				header.SampleRate = binary.LittleEndian.Uint32(data[offset+12 : offset+16])
				header.ByteRate = binary.LittleEndian.Uint32(data[offset+16 : offset+20])
				header.BlockAlign = binary.LittleEndian.Uint16(data[offset+20 : offset+22])
				header.BitsPerSample = binary.LittleEndian.Uint16(data[offset+22 : offset+24])
			}
		} else if chunkID == "data" {
			copy(header.Subchunk2ID[:], data[offset:offset+4])
			header.Subchunk2Size = chunkSize
			audioStart := offset + 8
			audioEnd := audioStart + int(chunkSize)
			if audioEnd > len(data) {
				audioEnd = len(data)
			}
			return header, data[audioStart:audioEnd], nil
		}

		// 移动到下一个 chunk（chunk 大小需要对齐到偶数）
		offset += 8 + int(chunkSize)
		if chunkSize%2 == 1 {
			offset++ // padding byte
		}
	}

	// 如果没找到 data chunk，回退到固定 44 字节头
	reader := bytes.NewReader(data)
	if err := binary.Read(reader, binary.LittleEndian, header); err != nil {
		return nil, nil, err
	}
	return header, data[44:], nil
}

// WriteWAV 写入 WAV 文件
func WriteWAV(filepath string, header *WAVHeader, audioData []byte) error {
	f, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer f.Close()

	// 更新头信息中的大小
	header.Subchunk2Size = uint32(len(audioData))
	header.ChunkSize = 36 + header.Subchunk2Size

	if err := binary.Write(f, binary.LittleEndian, header); err != nil {
		return err
	}

	_, err = f.Write(audioData)
	return err
}

// GenerateSilence 生成指定时长的静音数据
func GenerateSilence(header *WAVHeader, seconds float64) []byte {
	numSamples := int(float64(header.SampleRate) * seconds)
	bytesPerSample := int(header.BitsPerSample / 8)
	numChannels := int(header.NumChannels)

	silenceSize := numSamples * bytesPerSample * numChannels
	return make([]byte, silenceSize)
}

// ConcatAudio 合并多个音频数据
func ConcatAudio(parts ...[]byte) []byte {
	var total int
	for _, p := range parts {
		total += len(p)
	}

	result := make([]byte, 0, total)
	for _, p := range parts {
		result = append(result, p...)
	}
	return result
}

// ConcatWAVFiles 合并多个 WAV 文件，支持插入静音
type AudioSegment struct {
	FilePath      string  // WAV 文件路径（如果是静音则为空）
	SilenceSeconds float64 // 静音时长（如果是文件则为0）
}

func ConcatWAVFiles(segments []AudioSegment, outputPath string) error {
	if len(segments) == 0 {
		return fmt.Errorf("没有音频片段")
	}

	// 先找到第一个音频文件，获取 header 信息
	var baseHeader *WAVHeader
	for _, seg := range segments {
		if seg.FilePath != "" {
			header, _, err := ReadWAV(seg.FilePath)
			if err != nil {
				return fmt.Errorf("读取 %s 失败: %w", seg.FilePath, err)
			}
			baseHeader = header
			break
		}
	}

	if baseHeader == nil {
		return fmt.Errorf("没有有效的音频文件")
	}

	var allAudioData []byte

	for _, seg := range segments {
		if seg.SilenceSeconds > 0 {
			// 生成静音
			silence := GenerateSilence(baseHeader, seg.SilenceSeconds)
			allAudioData = append(allAudioData, silence...)
		} else if seg.FilePath != "" {
			// 读取 WAV 文件
			_, audioData, err := ReadWAV(seg.FilePath)
			if err != nil {
				return fmt.Errorf("读取 %s 失败: %w", seg.FilePath, err)
			}
			allAudioData = append(allAudioData, audioData...)
		}
	}

	return WriteWAV(outputPath, baseHeader, allAudioData)
}

// ReadWAVFromReader 从 io.Reader 读取 WAV
// 支持带有额外 chunks 的 WAV 文件（如 LIST、fact 等）
func ReadWAVFromReader(r io.Reader) (*WAVHeader, []byte, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, err
	}

	if len(data) < 44 {
		return nil, nil, fmt.Errorf("WAV 文件太小")
	}

	// 验证 RIFF 头
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, nil, fmt.Errorf("无效的 WAV 文件格式")
	}

	header := &WAVHeader{}
	copy(header.ChunkID[:], data[0:4])
	header.ChunkSize = binary.LittleEndian.Uint32(data[4:8])
	copy(header.Format[:], data[8:12])

	// 查找 fmt 和 data chunks
	offset := 12
	for offset < len(data)-8 {
		chunkID := string(data[offset : offset+4])
		chunkSize := binary.LittleEndian.Uint32(data[offset+4 : offset+8])

		if chunkID == "fmt " {
			copy(header.Subchunk1ID[:], data[offset:offset+4])
			header.Subchunk1Size = chunkSize
			if offset+8+int(chunkSize) <= len(data) && chunkSize >= 16 {
				header.AudioFormat = binary.LittleEndian.Uint16(data[offset+8 : offset+10])
				header.NumChannels = binary.LittleEndian.Uint16(data[offset+10 : offset+12])
				header.SampleRate = binary.LittleEndian.Uint32(data[offset+12 : offset+16])
				header.ByteRate = binary.LittleEndian.Uint32(data[offset+16 : offset+20])
				header.BlockAlign = binary.LittleEndian.Uint16(data[offset+20 : offset+22])
				header.BitsPerSample = binary.LittleEndian.Uint16(data[offset+22 : offset+24])
			}
		} else if chunkID == "data" {
			copy(header.Subchunk2ID[:], data[offset:offset+4])
			header.Subchunk2Size = chunkSize
			audioStart := offset + 8
			audioEnd := audioStart + int(chunkSize)
			if audioEnd > len(data) {
				audioEnd = len(data)
			}
			return header, data[audioStart:audioEnd], nil
		}

		// 移动到下一个 chunk（chunk 大小需要对齐到偶数）
		offset += 8 + int(chunkSize)
		if chunkSize%2 == 1 {
			offset++ // padding byte
		}
	}

	// 如果没找到 data chunk，回退到固定 44 字节头
	reader := bytes.NewReader(data)
	if err := binary.Read(reader, binary.LittleEndian, header); err != nil {
		return nil, nil, err
	}
	return header, data[44:], nil
}
