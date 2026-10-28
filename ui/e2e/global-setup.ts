import { rmSync } from "node:fs";

export default function globalSetup() {
  rmSync("e2e/.auth", { recursive: true, force: true });
}
