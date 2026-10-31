<script lang="ts">
	import { onMount } from 'svelte';
	import { health } from '#lib/api/client.js';

	let status = $state('checking…');

	onMount(async () => {
		try {
			status = (await health()).status;
		} catch (error) {
			status = `unreachable (${error instanceof Error ? error.message : String(error)})`;
		}
	});
</script>

<section>
	<h1>Walking skeleton</h1>
	<p>API health: <span data-testid="api-status">{status}</span></p>
</section>
