package music

import (
	"bytes"
	"errors"
	"fmt"
	"io"
)

var errInvalidOggPage = errors.New("invalid ogg page")

type oggOpusReader struct {
	reader  io.Reader
	packet  bytes.Buffer
	pending [][]byte
}

func newOggOpusReader(reader io.Reader) *oggOpusReader {
	return &oggOpusReader{reader: reader}
}

func (r *oggOpusReader) ProvideOpusFrame() ([]byte, error) {
	for {
		if len(r.pending) > 0 {
			packet := r.pending[0]
			r.pending = r.pending[1:]
			if isOpusHeaderPacket(packet) {
				continue
			}
			return packet, nil
		}
		if err := r.readPage(); err != nil {
			return nil, err
		}
	}
}

func (*oggOpusReader) Close() {}

func (r *oggOpusReader) readPage() error {
	var header [27]byte
	if _, err := io.ReadFull(r.reader, header[:]); err != nil {
		return err
	}
	if string(header[0:4]) != "OggS" || header[4] != 0 {
		return errInvalidOggPage
	}
	segmentCount := int(header[26])
	segments := make([]byte, segmentCount)
	if _, err := io.ReadFull(r.reader, segments); err != nil {
		return err
	}
	dataLength := 0
	for _, segment := range segments {
		dataLength += int(segment)
	}
	pageData := make([]byte, dataLength)
	if _, err := io.ReadFull(r.reader, pageData); err != nil {
		return err
	}
	offset := 0
	for _, segment := range segments {
		length := int(segment)
		if offset+length > len(pageData) {
			return fmt.Errorf("%w: segment length exceeds page data", errInvalidOggPage)
		}
		if length > 0 {
			r.packet.Write(pageData[offset : offset+length])
		}
		offset += length
		if segment < 255 {
			packet := append([]byte(nil), r.packet.Bytes()...)
			r.packet.Reset()
			if len(packet) > 0 {
				r.pending = append(r.pending, packet)
			}
		}
	}
	return nil
}

func isOpusHeaderPacket(packet []byte) bool {
	return bytes.HasPrefix(packet, []byte("OpusHead")) || bytes.HasPrefix(packet, []byte("OpusTags"))
}
