package sem

import "context"

// ProviderOperation owns captured source through query rendering. It is not a
// persistent snapshot or a filesystem-wide atomic revision.
type ProviderOperation struct{ source sourceContext }

func OpenProviderOperation(ctx context.Context, repo string, options ProviderSnapshotOptions) (*ProviderOperation, error) {
	options.captureInputs = true
	source, err := prepareSource(ctx, repo, options)
	if err != nil {
		return nil, err
	}
	return &ProviderOperation{source: source}, nil
}
func (operation *ProviderOperation) Options(options ProviderSnapshotOptions) ProviderSnapshotOptions {
	options.captured = &operation.source
	return options
}
func (operation *ProviderOperation) Read(path string) (string, bool) {
	return operation.source.read(path)
}
func (operation *ProviderOperation) Close() error {
	if operation.source.close != nil {
		return operation.source.close()
	}
	return nil
}

// Finish verifies capture storage after the final resolver/renderer read.
func (operation *ProviderOperation) Finish() (*OperationInputManifest, error) {
	return operation.source.finishCapture(operation.source.paths)
}
