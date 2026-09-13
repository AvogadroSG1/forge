import { startTelemetry } from '$lib/telemetry';

// SvelteKit runs client hooks in the browser only, so SSR and prerendering
// never load the OpenTelemetry web SDK.
startTelemetry();
