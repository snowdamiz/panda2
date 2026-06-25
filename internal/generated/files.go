package generated

type File struct {
	Filename string
	MIMEType string
	Data     []byte
	AltText  string
}

type ImageReference struct {
	ID        string `json:"id"`
	Filename  string `json:"filename,omitempty"`
	MIMEType  string `json:"mime_type,omitempty"`
	URL       string `json:"url,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
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

func CloneImageReferences(references []ImageReference) []ImageReference {
	if len(references) == 0 {
		return nil
	}
	cloned := make([]ImageReference, 0, len(references))
	for _, reference := range references {
		cloned = append(cloned, reference)
	}
	return cloned
}
