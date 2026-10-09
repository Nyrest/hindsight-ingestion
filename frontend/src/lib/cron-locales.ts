import cronstrueModule from "cronstrue";
import "cronstrue/locales/de";
import "cronstrue/locales/es";
import "cronstrue/locales/fr";
import "cronstrue/locales/id";
import "cronstrue/locales/it";
import "cronstrue/locales/ja";
import "cronstrue/locales/ko";
import "cronstrue/locales/pl";
import "cronstrue/locales/pt_BR";
import "cronstrue/locales/ru";
import "cronstrue/locales/tr";
import "cronstrue/locales/zh_CN";
import "cronstrue/locales/zh_TW";

// The UMD build exposes the descriptor beneath default in some bundlers.
const cronstrue = (cronstrueModule as unknown as { default?: typeof cronstrueModule }).default ?? cronstrueModule;
type Locale = (typeof cronstrue.locales)[string];

// Correct awkward upstream phrases through the library's locale interface.
Object.assign(cronstrue.locales.ja, {
  atSpace: () => "実行時刻: ",
  at: () => "実行時刻: ",
  atX0: () => "実行時刻: %s",
  onTheHour: () => "正時",
  commaOnlyOnX0: () => "、%sのみ",
  commaX0ThroughX1: () => "、%sから%sまで",
  commaAndX0ThroughX1: () => "、%sから%sまで",
  commaOnlyInX0: () => "、%sのみ",
  commaOnTheLastDayOfTheMonth: () => "、毎月の最終日",
  commaOnTheLastWeekdayOfTheMonth: () => "、毎月の最後の平日",
  commaOnDayX0OfTheMonth: () => "、毎月%s日",
  commaBetweenDayX0AndX1OfTheMonth: () => "、毎月%s日から%s日まで",
} satisfies Partial<Locale>);

Object.assign(cronstrue.locales.pl, {
  commaOnlyOnX0: () => ", tylko w %s",
  commaOnDayX0OfTheMonth: () => ", %s. dnia miesiąca",
  daysOfTheWeekInCase: (form?: number) => form
    ? ["niedzieli", "poniedziałku", "wtorku", "środy", "czwartku", "piątku", "soboty"]
    : ["niedzielę", "poniedziałek", "wtorek", "środę", "czwartek", "piątek", "sobotę"],
} satisfies Partial<Locale>);

Object.assign(cronstrue.locales.it, {
  commaX0ThroughX1: () => ", da %s a %s",
  commaAndX0ThroughX1: () => ", e da %s a %s",
  commaOnTheLastWeekdayOfTheMonth: () => ", nell’ultimo giorno feriale del mese",
} satisfies Partial<Locale>);

Object.assign(cronstrue.locales.pt_BR, {
  commaOnlyOnX0: () => ", apenas %s",
  onTheHour: () => "Na hora exata",
  commaOnTheLastWeekdayOfTheMonth: () => ", no último dia útil do mês",
} satisfies Partial<Locale>);

export default cronstrue;
