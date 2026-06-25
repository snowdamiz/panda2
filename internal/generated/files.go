package generated

type File struct {
	Filename string
	MIMEType string
	Data     []byte
	AltText  string
}

func (f File) SizeBytes() int64 {
	return int64(len(f.Data))
}

func CloneFiles(files []File) []File {
	if len(files) == 0 {
		return nil
	}
	cloned := make([]File, 0, len(files))
	for _, file := range files {
		next := file
		if file.Data != nil {
			next.Data = append([]byte(nil), file.Data...)
		}
		cloned = append(cloned, next)
	}
	return cloned
}
