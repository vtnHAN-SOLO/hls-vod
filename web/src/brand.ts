// The running app's brand. vite.config.ts resolved and defaulted both values
// at build time (src/lib/brand.ts), so they are always set here.
import { type Brand, resolveBrand } from "./lib/brand";

export const brand: Brand = resolveBrand({
  VITE_BRAND_NAME: import.meta.env.VITE_BRAND_NAME as string | undefined,
  VITE_BRAND_ACCENT: import.meta.env.VITE_BRAND_ACCENT as string | undefined,
});
