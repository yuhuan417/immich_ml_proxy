package tasks

import "testing"

func TestNormalizeTaskName(t *testing.T) {
	if got := NormalizeTaskName(LegacyFacialRecognitionTask); got != FacialRecognitionTask {
		t.Fatalf("expected legacy task name to normalize to %q, got %q", FacialRecognitionTask, got)
	}
	if got := NormalizeTaskName("clip"); got != "clip" {
		t.Fatalf("expected unrelated task name to stay unchanged, got %q", got)
	}
}

func TestShouldSplitTask(t *testing.T) {
	if !ShouldSplitTask(ClipTask) {
		t.Fatal("expected clip task to be split")
	}
	if ShouldSplitTask(OCRTask) {
		t.Fatal("did not expect ocr task to be split")
	}
}
