import { writable, get } from "svelte/store";

export interface LicenseInfo {
  edition: string;
  company: string;
  users: number;
  expires_at: string;
  features: string[];
}

export const license = writable<LicenseInfo>({
  edition: "community",
  company: "",
  users: 0,
  expires_at: "",
  features: [],
});

export async function loadLicense() {
  // Community edition — no license endpoint needed
}

export function hasFeature(_feature: string): boolean {
  return false;
}

export function isEnterprise(): boolean {
  return false;
}
