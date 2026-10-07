import { Check, ChevronsUpDown } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { browserTimeZone, getTimeZones } from "@/lib/format";
import { cn } from "@/lib/utils";

export function TimezoneCombobox({
  value,
  onChange,
  id,
  invalid,
}: {
  value: string;
  onChange: (tz: string) => void;
  id?: string;
  invalid?: boolean;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const zones = useMemo(() => {
    const all = getTimeZones();
    return value && !all.includes(value) ? [value, ...all] : all;
  }, [value]);
  const local = browserTimeZone();

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          id={id}
          type="button"
          variant="outline"
          role="combobox"
          aria-expanded={open}
          aria-invalid={invalid}
          className="w-full justify-between font-normal"
        >
          <span className="truncate">{value || t("schedule.selectTimezone")}</span>
          <ChevronsUpDown className="opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-(--radix-popover-trigger-width) min-w-64 p-0" align="start">
        <Command>
          <CommandInput placeholder={t("schedule.searchTimezone")} />
          <CommandList>
            <CommandEmpty>{t("schedule.noTimezone")}</CommandEmpty>
            <CommandGroup heading={t("schedule.suggested")}>
              {Array.from(new Set(["UTC", local])).map((tz) => (
                <TzItem key={`s-${tz}`} tz={tz} value={value} onSelect={(v) => (onChange(v), setOpen(false))} />
              ))}
            </CommandGroup>
            <CommandGroup heading={t("schedule.allTimezones")}>
              {zones.map((tz) => (
                <TzItem key={tz} tz={tz} value={value} onSelect={(v) => (onChange(v), setOpen(false))} />
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}

function TzItem({ tz, value, onSelect }: { tz: string; value: string; onSelect: (tz: string) => void }) {
  return (
    <CommandItem value={tz} onSelect={() => onSelect(tz)}>
      <Check className={cn("size-4", value === tz ? "opacity-100" : "opacity-0")} />
      {tz}
    </CommandItem>
  );
}
