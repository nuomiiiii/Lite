package metric

import (
	"compress/flate"
	"io"
	"sync"
)

var (
	flateWriterPools [flate.BestCompression - flate.HuffmanOnly + 1]sync.Pool
	flateReaderPool  sync.Pool
)

type emptyFlateSource struct{}

func (emptyFlateSource) Read([]byte) (int, error) { return 0, io.EOF }
func (emptyFlateSource) ReadByte() (byte, error)  { return 0, io.EOF }

func acquireFlateWriter(dst io.Writer, level int) (*flate.Writer, error) {
	if level >= flate.HuffmanOnly && level <= flate.BestCompression {
		if pooled := flateWriterPools[level-flate.HuffmanOnly].Get(); pooled != nil {
			writer := pooled.(*flate.Writer)
			writer.Reset(dst)
			return writer, nil
		}
	}
	return flate.NewWriter(dst, level)
}

func releaseFlateWriter(level int, writer *flate.Writer) {
	if writer == nil || level < flate.HuffmanOnly || level > flate.BestCompression {
		return
	}
	writer.Reset(io.Discard)
	flateWriterPools[level-flate.HuffmanOnly].Put(writer)
}

func acquireFlateReader(src io.Reader) io.ReadCloser {
	if pooled := flateReaderPool.Get(); pooled != nil {
		reader := pooled.(io.ReadCloser)
		if resetter, ok := reader.(flate.Resetter); ok {
			if err := resetter.Reset(src, nil); err == nil {
				return reader
			}
		}
	}
	return flate.NewReader(src)
}

func releaseFlateReader(reader io.ReadCloser) {
	if reader == nil {
		return
	}
	resetter, ok := reader.(flate.Resetter)
	if !ok {
		return
	}
	if err := resetter.Reset(emptyFlateSource{}, nil); err != nil {
		return
	}
	flateReaderPool.Put(reader)
}
