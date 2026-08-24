package control

import "workground2/internal/imageutil"

// maxVisionDim caps the longest image side sent to a model. Kept as a name so
// the compression tests can assert the downscale budget; the rule itself lives
// in imageutil so the controller and the view_image tool share one compressor.
const maxVisionDim = imageutil.MaxVisionDim

// compressForVision downscales an oversized image and re-encodes it before
// base64. It delegates to the shared imageutil.Compress so attachment handling
// and the view_image tool apply identical rules.
func compressForVision(raw []byte, mime string) ([]byte, string) {
	return imageutil.Compress(raw, mime)
}
