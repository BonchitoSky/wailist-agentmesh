// A workflow's schedule in words a person reads, not the cron that runs it.
//
// The backend stores schedules as standard 5-field cron in UTC
// (minute hour day-of-month month day-of-week), and the workflow screen used
// to print that as is -- "0 7 * * 1-5", which says nothing to anyone who has
// not read a crontab. This turns the common shapes into "Every weekday at
// 12:30 PM", in the reader's own time zone, and anything else into a plain
// "On a custom schedule" rather than the raw expression.
//
// Times are converted the way lib/cronCadence.ts converts them: build the real
// UTC instant, then read it back in the target zone, so the calendar and any
// daylight-saving shift are the platform's to get right. A day list moves
// with the time when local time falls on the other side of midnight.

const CUSTOM = "On a custom schedule";
const DAY_NAMES = [
  "Sunday",
  "Monday",
  "Tuesday",
  "Wednesday",
  "Thursday",
  "Friday",
  "Saturday",
];
const DAY_SHORT = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

function asInt(field: string, min: number, max: number): number | null {
  if (!/^\d+$/.test(field)) return null;
  const n = Number(field);
  return n >= min && n <= max ? n : null;
}

function everyN(field: string): number | null {
  const match = /^\*\/(\d+)$/.exec(field);
  return match && Number(match[1]) > 0 ? Number(match[1]) : null;
}

function every(n: number, unit: string): string {
  return n === 1 ? `Every ${unit}` : `Every ${n} ${unit}s`;
}

// Day-of-week: numbers, ranges and lists, 0 or 7 for Sunday. Names and steps
// are rare enough to be "custom".
function parseDays(field: string): number[] | null {
  const days = new Set<number>();
  for (const part of field.split(",")) {
    const range = /^(\d)-(\d)$/.exec(part);
    const from = range ? Number(range[1]) : asInt(part, 0, 7);
    const to = range ? Number(range[2]) : from;
    if (from === null || to === null || from > to || to > 7) return null;
    for (let d = from; d <= to; d++) days.add(d % 7);
  }
  return [...days];
}

function phraseDays(days: number[]): string {
  const key = days.join(",");
  if (days.length === 7) return "Every day";
  if (key === "1,2,3,4,5") return "Every weekday";
  if (key === "0,6") return "Every weekend";
  if (days.length === 1) return `Every ${DAY_NAMES[days[0]]}`;
  const names = days.map((d) => DAY_SHORT[d]);
  return `Every ${names.slice(0, -1).join(", ")} and ${names.at(-1)}`;
}

function ordinal(n: number): string {
  const teen = n % 100 >= 11 && n % 100 <= 13;
  const suffix = teen ? "th" : (["th", "st", "nd", "rd"][n % 10] ?? "th");
  return `${n}${suffix}`;
}

/**
 * The schedule in plain words, in `timeZone` (the reader's own when left out).
 * `now` anchors the week and month the times are computed in, so a
 * daylight-saving change is reflected once it is in effect.
 */
export function describeSchedule(
  cron: string,
  now: Date = new Date(),
  timeZone?: string,
): string {
  const parts = cron.trim().split(/\s+/);
  if (parts.length !== 5) return CUSTOM;
  const [minute, hour, dom, month, dow] = parts;
  if (month !== "*") return CUSTOM;

  // Repeating intervals: no time of day to convert.
  if (dom === "*" && dow === "*") {
    if (minute === "*" && hour === "*") return "Every minute";
    const minutes = everyN(minute);
    if (minutes !== null && hour === "*") return every(minutes, "minute");
    if (asInt(minute, 0, 59) !== null) {
      if (hour === "*") return "Every hour";
      const hours = everyN(hour);
      if (hours !== null) return every(hours, "hour");
    }
  }

  const m = asInt(minute, 0, 59);
  const h = asInt(hour, 0, 23);
  if (m === null || h === null) return CUSTOM;

  const read = (instant: Date, options: Intl.DateTimeFormatOptions) =>
    new Intl.DateTimeFormat("en-US", { ...options, timeZone }).format(instant);
  // Some ICU versions put a narrow no-break space before AM/PM.
  const at = (instant: Date) =>
    read(instant, { hour: "numeric", minute: "2-digit" }).replace(/\s/g, " ");
  const year = now.getUTCFullYear();
  const mon = now.getUTCMonth();
  const date = now.getUTCDate();

  if (dom === "*" && dow === "*") {
    return `Every day at ${at(new Date(Date.UTC(year, mon, date, h, m)))}`;
  }

  if (dom === "*") {
    const utcDays = parseDays(dow);
    if (!utcDays) return CUSTOM;
    const instants = utcDays.map(
      (d) => new Date(Date.UTC(year, mon, date - now.getUTCDay() + d, h, m)),
    );
    const localDays = [
      ...new Set(
        instants.map((i) => DAY_SHORT.indexOf(read(i, { weekday: "short" }))),
      ),
    ].sort((a, b) => a - b);
    return `${phraseDays(localDays)} at ${at(instants[0])}`;
  }

  // Monthly. One sampled month is not enough: Date.UTC rolls a day the month
  // lacks (the 31st in September) into the next month, and a day near
  // midnight can land on a different local day depending on the month's
  // length. So every month of the year is checked -- skipping months that do
  // not have the day, as the scheduler does -- and a single local day is
  // named only when every month agrees on it.
  const day = asInt(dom, 1, 31);
  if (dow === "*" && day !== null) {
    const instants = Array.from(
      { length: 12 },
      (_, month) => new Date(Date.UTC(year, month, day, h, m)),
    ).filter((instant) => instant.getUTCDate() === day);
    const localDays = new Set(
      instants.map((instant) => read(instant, { day: "numeric" })),
    );
    if (instants.length === 0 || localDays.size !== 1) return CUSTOM;
    const localDay = Number([...localDays][0]);
    return `Every month on the ${ordinal(localDay)} at ${at(instants[0])}`;
  }

  return CUSTOM;
}
