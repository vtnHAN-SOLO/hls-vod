// The site's own pages: vite.config.ts turns each .md into its parsed tree.
declare module "*.md" {
  const doc: import("../lib/markdown").Doc;
  export default doc;
}
