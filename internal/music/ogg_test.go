package music

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestOggOpusReaderSkipsHeadersAndReturnsAudioPackets(t *testing.T) {
	var data bytes.Buffer
	if err := writeTestOggPage(&data, 0, []byte("OpusHead"), []byte("OpusTags"), []byte{0x01, 0x02, 0x03}); err != nil {
		t.Fatalf("write test ogg page: %v", err)
	}
	if err := writeTestOggPage(&data, 1, []byte{0x04, 0x05}); err != nil {
		t.Fatalf("write test ogg page: %v", err)
	}

	reader := newOggOpusReader(&data)
	frame, err := reader.ProvideOpusFrame()
	if err != nil {
		t.Fatalf("first frame error: %v", err)
	}
	if !bytes.Equal(frame, []byte{0x01, 0x02, 0x03}) {
		t.Fatalf("unexpected first frame %#v", frame)
	}
	frame, err = reader.ProvideOpusFrame()
	if err != nil {
		t.Fatalf("second frame error: %v", err)
	}
	if !bytes.Equal(frame, []byte{0x04, 0x05}) {
		t.Fatalf("unexpected second frame %#v", frame)
	}
	if _, err = reader.ProvideOpusFrame(); err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func writeTestOggPage(w io.Writer, sequence uint32, packets ...[]byte) error {
	var segments []byte
	var data []byte
	for _, packet := range packets {
		remaining := packet
		for len(remaining) >= 255 {
			segments = append(segments, 255)
			data = append(data, remaining[:255]...)
			remaining = remaining[255:]
		}
		segments = append(segments, byte(len(remaining)))
		data = append(data, remaining...)
	}
	if len(segments) > 255 {
		return errors.New("too many ogg segments")
	}
	header := make([]byte, 27)
	copy(header, "OggS")
	header[4] = 0
	header[26] = byte(len(segments))
	binary.LittleEndian.PutUint32(header[18:22], sequence)
	if _, err := w.Write(header); err != nil {
		return err
	}
	if _, err := w.Write(segments); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}
