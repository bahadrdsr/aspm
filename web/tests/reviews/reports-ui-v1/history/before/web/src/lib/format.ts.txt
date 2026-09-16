export function label(value: string): string {
  return value.replaceAll("-", " ").replace(/\b[a-z]/g, (letter) => letter.toUpperCase());
}

const day = new Intl.DateTimeFormat("en", { month: "short", day: "numeric", year: "numeric", timeZone: "UTC" });
const instant = new Intl.DateTimeFormat("en", { month: "short", day: "numeric", year: "numeric", hour: "2-digit", minute: "2-digit", timeZone: "UTC", timeZoneName: "short" });

export function sourceDate(value: string | null): string {
  return value === null ? "Unknown" : day.format(new Date(value));
}

export function timestampLabel(value: string | null): string {
  return value === null ? "Unknown source time" : instant.format(new Date(value));
}
