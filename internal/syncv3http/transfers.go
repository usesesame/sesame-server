package syncv3http

import "sync"

const (
	defaultUploadBudgetBytes = 48 << 20
	defaultDownloadLimit     = 8
)

type transferGate struct {
	mu            sync.Mutex
	uploadBudget  int64
	uploadBytes   int64
	uploading     map[string]struct{}
	downloadLimit int
	downloads     int
	downloading   map[string]int
}

func newTransferGate(uploadBudget int64, downloadLimit int) *transferGate {
	return &transferGate{
		uploadBudget:  max(uploadBudget, smallBodyBytes),
		uploading:     map[string]struct{}{},
		downloadLimit: max(downloadLimit, 1),
		downloading:   map[string]int{},
	}
}

func uploadWeight(declared int64) int64 {
	if declared < 0 || declared > snapshotBodyBytes {
		return snapshotBodyBytes
	}
	return max(declared, smallBodyBytes)
}

func (gate *transferGate) beginUpload(owner string, declared int64) (func(), bool) {
	weight := uploadWeight(declared)
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if _, busy := gate.uploading[owner]; busy || gate.uploadBytes+weight > gate.uploadBudget {
		return nil, false
	}
	gate.uploading[owner] = struct{}{}
	gate.uploadBytes += weight
	return func() {
		gate.mu.Lock()
		defer gate.mu.Unlock()
		delete(gate.uploading, owner)
		gate.uploadBytes -= weight
	}, true
}

func (gate *transferGate) ownerDownloadLimit() int {
	return max(gate.downloadLimit/2, 1)
}

func (gate *transferGate) beginDownload(owner string) (func(), bool) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.downloads >= gate.downloadLimit || gate.downloading[owner] >= gate.ownerDownloadLimit() {
		return nil, false
	}
	gate.downloads++
	gate.downloading[owner]++
	return func() {
		gate.mu.Lock()
		defer gate.mu.Unlock()
		gate.downloads--
		if gate.downloading[owner] <= 1 {
			delete(gate.downloading, owner)
			return
		}
		gate.downloading[owner]--
	}, true
}
