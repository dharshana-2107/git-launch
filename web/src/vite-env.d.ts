/// <reference types="vite/client" />

// Optional settings, read from the environment or web/.env
interface ImportMetaEnv {
  readonly VITE_APPS_DOMAIN?: string; // default "localhost"
  readonly VITE_APPS_PORT?: string; // set only if Traefik is not on port 80
}
