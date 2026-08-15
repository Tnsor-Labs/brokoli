// Connection catalog metadata helpers — the single place that absorbs the
// /api/connection-types payload's optional fields, so the catalog renders
// sensibly for types the server (or a plugin) knows but this build's icon
// set does not.

export interface ConnTypeMeta {
  type: string;
  label: string;
  category?: string;
  description?: string;
  icon?: string;
  fields: string[];
  hints?: Record<string, string>;
}

export const CATEGORY_ORDER = ["database", "storage", "api", "other"] as const;

const CATEGORY_LABELS: Record<string, string> = {
  database: "Databases",
  storage: "Storage",
  api: "APIs & Files",
  other: "Other",
};

export function categoryLabel(category: string): string {
  return CATEGORY_LABELS[category] || CATEGORY_LABELS.other;
}

/** Fallback one-liners for servers that predate the description field. */
const FALLBACK_DESCRIPTIONS: Record<string, string> = {
  postgres: "Open-source relational database",
  mysql: "Open-source relational database",
  sqlite: "Embedded single-file database",
  http: "Any HTTP endpoint or REST API",
  sftp: "File transfer over SSH",
  generic: "Any other system — bring your own settings",
};

export function describe(meta: ConnTypeMeta): string {
  return meta.description || FALLBACK_DESCRIPTIONS[meta.type] || "External connection";
}

/** "PostgreSQL" -> "PG", "Amazon S3" -> "S3" — the .hero-mark monogram
 * convention, used as the tile fallback when no glyph exists. */
export function monogramFor(label: string): string {
  const words = label
    .replace(/[^A-Za-z0-9 ]/g, " ")
    .split(/\s+/)
    .filter(Boolean);
  if (words.length === 0) return "??";
  if (words.length === 1) {
    const word = words[0];
    const caps = word.slice(1).replace(/[^A-Z0-9]/g, "");
    return (word[0] + (caps[0] || word[1] || "")).toUpperCase();
  }
  // Prefer a short final token that looks like a product qualifier (S3, DB).
  const last = words[words.length - 1];
  if (last.length <= 3 && /[0-9]/.test(last)) return last.toUpperCase();
  return (words[0][0] + last[0]).toUpperCase();
}

export interface CatalogGroup {
  category: string;
  label: string;
  types: ConnTypeMeta[];
}

export function groupByCategory(types: ConnTypeMeta[]): CatalogGroup[] {
  const buckets = new Map<string, ConnTypeMeta[]>();
  for (const t of types) {
    const cat = CATEGORY_ORDER.includes((t.category || "") as (typeof CATEGORY_ORDER)[number])
      ? (t.category as string)
      : "other";
    if (!buckets.has(cat)) buckets.set(cat, []);
    buckets.get(cat)!.push(t);
  }
  return CATEGORY_ORDER.filter((cat) => buckets.has(cat)).map((cat) => ({
    category: cat,
    label: categoryLabel(cat),
    types: buckets.get(cat)!,
  }));
}
