package postbox

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

const objectOrderKey = "\x00postbox_order"

// Resource limits apply across nested objects, not independently per child.
const (
	maxDecodeDepth = 64
	maxDecodeItems = 1 << 20
)

type byteReader struct {
	data []byte
	off  int
}

func newByteReader(data []byte) *byteReader {
	return &byteReader{data: data}
}

func (r *byteReader) remaining() int {
	return len(r.data) - r.off
}

func (r *byteReader) count(minBytes int) (int32, error) {
	count, err := r.int32()
	if err != nil {
		return 0, err
	}
	if count < 0 || int64(count) > int64(r.remaining()/minBytes) || count > maxDecodeItems {
		return 0, errors.New("postbox count exceeds payload or resource limit")
	}
	return count, nil
}

func (r *byteReader) read(n int) ([]byte, error) {
	if n < 0 || r.remaining() < n {
		return nil, errors.New("short postbox payload")
	}
	out := r.data[r.off : r.off+n]
	r.off += n
	return out, nil
}

func (r *byteReader) int8() (int8, error) {
	data, err := r.read(1)
	if err != nil {
		return 0, err
	}
	return int8(data[0]), nil
}

func (r *byteReader) uint8() (uint8, error) {
	data, err := r.read(1)
	if err != nil {
		return 0, err
	}
	return data[0], nil
}

func (r *byteReader) int32() (int32, error) {
	data, err := r.read(4)
	if err != nil {
		return 0, err
	}
	return int32(binary.LittleEndian.Uint32(data)), nil
}

func (r *byteReader) uint32() (uint32, error) {
	data, err := r.read(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(data), nil
}

func (r *byteReader) int64() (int64, error) {
	data, err := r.read(8)
	if err != nil {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint64(data)), nil
}

func (r *byteReader) float64() (float64, error) {
	data, err := r.read(8)
	if err != nil {
		return 0, err
	}
	return math.Float64frombits(binary.LittleEndian.Uint64(data)), nil
}

func (r *byteReader) bytes() ([]byte, error) {
	size, err := r.int32()
	if err != nil {
		return nil, err
	}
	if size < 0 {
		return nil, errors.New("negative postbox byte length")
	}
	data, err := r.read(int(size))
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(data))
	copy(out, data)
	return out, nil
}

func (r *byteReader) string() (string, error) {
	data, err := r.bytes()
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (r *byteReader) shortString() (string, error) {
	size, err := r.uint8()
	if err != nil {
		return "", err
	}
	data, err := r.read(int(size))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// DecodeObject decodes a Postbox keyed object payload and returns the root "_"
// object when present. The representation intentionally mirrors the former
// Postbox maps carry string keys and an "@type" int64 discriminator.
func DecodeObject(data []byte) (map[string]any, error) {
	entries, err := DecodeEntries(data)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Key == "_" && entry.ValueType == 5 {
			if obj, ok := entry.Value.(map[string]any); ok {
				return obj, nil
			}
		}
	}
	return nil, nil
}

type Entry struct {
	Key       string
	ValueType int
	Value     any
}

func DecodeEntries(data []byte) ([]Entry, error) {
	items := maxDecodeItems
	d := decoder{reader: newByteReader(data), size: len(data), items: &items}
	return d.entries()
}

type decoder struct {
	reader *byteReader
	size   int
	depth  int
	items  *int
}

func (d decoder) claim(count int32) error {
	if int64(count) > int64(*d.items) {
		return errors.New("postbox item limit exceeded")
	}
	*d.items -= int(count)
	return nil
}

func (d decoder) count(minBytes int) (int32, error) {
	count, err := d.reader.count(minBytes)
	if err != nil {
		return 0, err
	}
	return count, d.claim(count)
}

func (d decoder) entries() ([]Entry, error) {
	var entries []Entry
	for d.reader.off < d.size {
		if err := d.claim(1); err != nil {
			return nil, err
		}
		key, err := d.reader.shortString()
		if err != nil {
			return nil, err
		}
		valueType, value, err := d.value()
		if err != nil {
			return nil, fmt.Errorf("decode %q: %w", key, err)
		}
		entries = append(entries, Entry{Key: key, ValueType: valueType, Value: value})
	}
	return entries, nil
}

func (d decoder) value() (int, any, error) {
	valueType, err := d.reader.uint8()
	if err != nil {
		return 0, nil, err
	}
	switch valueType {
	case 0:
		value, err := d.reader.int32()
		return int(valueType), int64(value), err
	case 1:
		value, err := d.reader.int64()
		return int(valueType), value, err
	case 2:
		value, err := d.reader.uint8()
		return int(valueType), value != 0, err
	case 3:
		value, err := d.reader.float64()
		return int(valueType), value, err
	case 4:
		value, err := d.reader.string()
		return int(valueType), value, err
	case 5:
		value, err := d.object()
		return int(valueType), value, err
	case 6:
		count, err := d.count(4)
		if err != nil {
			return int(valueType), nil, err
		}
		values := make([]any, 0, count)
		for range int(count) {
			value, err := d.reader.int32()
			if err != nil {
				return int(valueType), nil, err
			}
			values = append(values, int64(value))
		}
		return int(valueType), values, nil
	case 7:
		count, err := d.count(8)
		if err != nil {
			return int(valueType), nil, err
		}
		values := make([]any, 0, count)
		for range int(count) {
			value, err := d.reader.int64()
			if err != nil {
				return int(valueType), nil, err
			}
			values = append(values, value)
		}
		return int(valueType), values, nil
	case 8:
		count, err := d.count(8)
		if err != nil {
			return int(valueType), nil, err
		}
		values := make([]any, 0, count)
		for range int(count) {
			value, err := d.object()
			if err != nil {
				return int(valueType), nil, err
			}
			values = append(values, value)
		}
		return int(valueType), values, nil
	case 9:
		count, err := d.count(16)
		if err != nil {
			return int(valueType), nil, err
		}
		values := make([]any, 0, count)
		for range int(count) {
			left, err := d.object()
			if err != nil {
				return int(valueType), nil, err
			}
			right, err := d.object()
			if err != nil {
				return int(valueType), nil, err
			}
			values = append(values, []any{left, right})
		}
		return int(valueType), values, nil
	case 10:
		value, err := d.reader.bytes()
		return int(valueType), value, err
	case 11:
		return int(valueType), nil, nil
	case 12:
		count, err := d.count(4)
		if err != nil {
			return int(valueType), nil, err
		}
		values := make([]any, 0, count)
		for range int(count) {
			value, err := d.reader.string()
			if err != nil {
				return int(valueType), nil, err
			}
			values = append(values, value)
		}
		return int(valueType), values, nil
	case 13:
		count, err := d.count(4)
		if err != nil {
			return int(valueType), nil, err
		}
		values := make([]any, 0, count)
		for range int(count) {
			value, err := d.reader.bytes()
			if err != nil {
				return int(valueType), nil, err
			}
			values = append(values, value)
		}
		return int(valueType), values, nil
	default:
		return int(valueType), nil, fmt.Errorf("unknown postbox value type %d", valueType)
	}
}

func (d decoder) object() (map[string]any, error) {
	if d.depth >= maxDecodeDepth {
		return nil, errors.New("postbox nesting limit exceeded")
	}
	typeHash, err := d.reader.int32()
	if err != nil {
		return nil, err
	}
	size, err := d.reader.int32()
	if err != nil {
		return nil, err
	}
	if size < 0 {
		return nil, errors.New("negative postbox object size")
	}
	data, err := d.reader.read(int(size))
	if err != nil {
		return nil, err
	}
	child := decoder{reader: newByteReader(data), size: len(data), depth: d.depth + 1, items: d.items}
	entries, err := child.entries()
	if err != nil {
		return nil, err
	}
	payload := make(map[string]any, len(entries)+1)
	order := make([]string, 0, len(entries))
	for _, entry := range entries {
		payload[entry.Key] = entry.Value
		order = append(order, entry.Key)
	}
	payload[objectOrderKey] = order
	payload["@type"] = int64(typeHash)
	return payload, nil
}
