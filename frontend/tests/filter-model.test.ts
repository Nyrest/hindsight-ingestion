import { describe, expect, test } from "bun:test";
import { coerceValue, defaultRuleValue, isMultiOperator, isUnaryOperator, validateFilterGroup } from "../src/features/tasks/filter-model";
import { emptyTaskForm, formToInput, taskToForm } from "../src/features/tasks/task-form-model";
import type { FilterFieldSpec, FilterGroup, Task } from "../src/lib/types";

const fields: FilterFieldSpec[] = [
  { key: "name", label: "Name", type: "string", operators: ["equals", "regex", "exists", "containsAll"] },
  { key: "size", label: "Size", type: "number", operators: ["geq", "leq", "in"] },
  { key: "at", label: "Modified", type: "datetime", operators: ["leq"] },
];

describe("filter editing", () => {
  test("unary and multiple values use the right controls", () => {
    expect(isUnaryOperator("exists")).toBe(true);
    expect(defaultRuleValue(fields[0], "exists")).toBeNull();
    expect(coerceValue(fields[0], "exists", "notes")).toBeNull();
    expect(coerceValue(fields[0], "equals", null)).toBe("");
    expect(isMultiOperator("containsAll")).toBe(true);
    expect(coerceValue(fields[0], "containsAll", "notes")).toEqual(["notes"]);
  });

  test("nested errors retain their paths", () => {
    const group: FilterGroup = { rules: [], groups: [{ rules: [
      { field: "size", operator: "geq", value: Infinity },
      { field: "name", operator: "containsAll", value: [] },
      { field: "at", operator: "leq", value: "bad date" },
    ] }] };
    expect(validateFilterGroup(group, fields, 8)).toEqual({
      "expression.groups.0.rules.0": "filters.errors.number",
      "expression.groups.0.rules.1": "filters.errors.valueList",
      "expression.groups.0.rules.2": "filters.errors.date",
    });
    expect(validateFilterGroup({ rules: [], groups: [{ rules: [] }] }, fields, 8)["expression.groups.0"]).toBe("filters.errors.emptyGroup");
    expect(validateFilterGroup(group, fields, 1)["expression.groups.0"]).toBe("filters.errors.depth");
  });

  test("backend validates Go regex syntax; unary rules need no value", () => {
    expect(validateFilterGroup({ rules: [
      { field: "name", operator: "exists" },
      { field: "name", operator: "regex", value: "(?i)^notes" },
    ] }, fields, 8)).toEqual({});
  });

  test("nested groups and query survive save and reload without shared references", () => {
    const form = emptyTaskForm();
    form.filterMode = "advanced";
    form.advancedQuery = "trashed = false";
    form.filterGroup = { combinator: "or", rules: [], groups: [{ rules: [{ field: "size", operator: "leq", value: 2048 }] }] };
    const input = formToInput(form, {});
    expect(input.sourceFilter).toEqual({ mode: "advanced", expression: form.filterGroup, advancedQuery: form.advancedQuery });
    expect(input.sourceFilter.expression).toEqual(form.filterGroup);
    const reloaded = taskToForm({ ...input } as Task);
    expect(reloaded.filterGroup).toEqual(form.filterGroup);
    reloaded.filterGroup.groups![0].rules[0].value = 100;
    expect(form.filterGroup.groups![0].rules[0].value).toBe(2048);
  });
});
