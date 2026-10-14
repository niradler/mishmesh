package connections

type HostDiagnostic string

const (
	HostNotLoaded             HostDiagnostic = "connection is not loaded"
	HostLoadFailed            HostDiagnostic = "load connection failed"
	HostInvalidIdentity       HostDiagnostic = "invalid connection identity"
	HostIdentityChanged       HostDiagnostic = "connection identity changed during reload"
	HostUnknownType           HostDiagnostic = "unknown connection type"
	HostSecretsUnavailable    HostDiagnostic = "connection secrets unavailable"
	HostInvalidSecrets        HostDiagnostic = "invalid connection secrets"
	HostInvalidConfiguration  HostDiagnostic = "invalid connection configuration"
	HostInvalidProviderStatus HostDiagnostic = "invalid connection provider status"
	HostOpenFailed            HostDiagnostic = "open connection failed"
	HostDrainPending          HostDiagnostic = "previous connection generation is still draining; retry sync or restart the server"
	HostDrainFailed           HostDiagnostic = "connection reload could not drain the previous generation; retry sync or restart the server"
	HostSyncCanceled          HostDiagnostic = "connection sync canceled"
	HostSyncFailed            HostDiagnostic = "connection sync failed"
	HostSnapshotChanged       HostDiagnostic = "connection changed; reload before retrying"
	HostReloadCanceled        HostDiagnostic = "saved connection reload was canceled before admission; retry sync or save and restart"
	HostReloadTimedOut        HostDiagnostic = "saved connection reload timed out before admission; retry sync or save and restart"
	HostReloadUnavailable     HostDiagnostic = "saved connection reload could not enter the runtime; retry sync or save and restart"
	HostConfigurationPending  HostDiagnostic = "saved connection configuration is not loaded; retry sync or save and restart"
	HostRuntimeUnavailable    HostDiagnostic = "connection runtime unavailable; save and restart after enabling network control"
	HostSavedReloadFailed     HostDiagnostic = "saved connection could not reload; retry sync or save and restart"
	HostSavedSyncFailed       HostDiagnostic = "saved connection could not sync; retry sync or save and restart"
	HostSyncRetry             HostDiagnostic = "sync failed; retry sync or save and restart"
	HostOperationFailed       HostDiagnostic = "connection runtime operation failed; retry sync or save and restart"
)

func WithHostDiagnostic(status Status, diagnostic HostDiagnostic) Status {
	switch diagnostic {
	case HostNotLoaded, HostLoadFailed, HostInvalidIdentity, HostIdentityChanged, HostUnknownType,
		HostSecretsUnavailable, HostInvalidSecrets, HostInvalidConfiguration, HostInvalidProviderStatus, HostOpenFailed,
		HostDrainPending, HostDrainFailed, HostSyncCanceled, HostSyncFailed, HostSnapshotChanged,
		HostReloadCanceled, HostReloadTimedOut, HostReloadUnavailable, HostConfigurationPending, HostRuntimeUnavailable,
		HostSavedReloadFailed, HostSavedSyncFailed, HostSyncRetry, HostOperationFailed:
	default:
		diagnostic = HostOperationFailed
	}
	status.hostDiagnostic = diagnostic
	status.LastError = string(diagnostic)
	return status
}
