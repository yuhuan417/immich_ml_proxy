package tasks

const (
	ClipTask                        = "clip"
	OCRTask                         = "ocr"
	FacialRecognitionTask           = "facial-recognition"
	LegacyFacialRecognitionTask     = "facial_recognition"
)

// NormalizeTaskName keeps routing aligned with Immich task names while
// accepting the legacy facial_recognition spelling from older configs.
func NormalizeTaskName(task string) string {
	switch task {
	case LegacyFacialRecognitionTask:
		return FacialRecognitionTask
	default:
		return task
	}
}

// ShouldSplitTask returns whether a task should be forwarded as multiple
// per-type requests instead of one grouped request.
func ShouldSplitTask(task string) bool {
	return NormalizeTaskName(task) == ClipTask
}
