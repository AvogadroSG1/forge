import {
  ApplicationConfig,
  provideAppInitializer,
  provideBrowserGlobalErrorListeners,
} from '@angular/core';
import { provideRouter } from '@angular/router';

import { environment } from '../environments/environment';
import { routes } from './app.routes';
import { startTelemetry } from './telemetry';

export const appConfig: ApplicationConfig = {
  providers: [
    provideBrowserGlobalErrorListeners(),
    provideRouter(routes),
    provideAppInitializer(() => {
      startTelemetry({
        otlpEndpoint: environment.otlpEndpoint,
        serviceName: environment.serviceName || undefined,
      });
    }),
  ],
};
