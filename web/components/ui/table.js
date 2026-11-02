import * as React from "react";

import { cn } from "@/lib/utils";

const TableAppearance = React.createContext("grid");

function Table({ className, appearance = "grid", ...props }) {
  return (
    <TableAppearance.Provider value={appearance}>
    <div data-slot="table-container" className="relative w-full overflow-x-auto">
      <table
        data-slot="table"
        data-appearance={appearance}
        className={cn("w-full caption-bottom text-sm", appearance === "cards" && "border-separate border-spacing-x-0 border-spacing-y-4", className)}
        {...props}
      />
    </div>
    </TableAppearance.Provider>
  );
}

function TableHeader({ className, ...props }) {
  const cards = React.useContext(TableAppearance) === "cards";
  return (
    <thead data-slot="table-header" className={cn(!cards && "[&_tr]:border-b", className)} {...props} />
  );
}

function TableBody({ className, ...props }) {
  const cards = React.useContext(TableAppearance) === "cards";
  return (
    <tbody
      data-slot="table-body"
      className={cn(!cards && "[&_tr:last-child]:border-0", className)}
      {...props}
    />
  );
}

function TableRow({ className, ...props }) {
  const cards = React.useContext(TableAppearance) === "cards";
  return (
    <tr
      data-slot="table-row"
      className={cn("group/row transition-colors", cards ? "" : "border-b hover:bg-hover-lift", className)}
      {...props}
    />
  );
}

function TableHead({ className, ...props }) {
  return (
    <th
      data-slot="table-head"
      className={cn(
        "h-10 px-3 text-left align-middle font-medium whitespace-nowrap text-muted-foreground",
        className
      )}
      {...props}
    />
  );
}

function TableCell({ className, ...props }) {
  const cards = React.useContext(TableAppearance) === "cards";
  return (
    <td
      data-slot="table-cell"
      className={cn("p-3 align-middle whitespace-nowrap", cards && "px-4 border-y border-border/55 bg-card first:rounded-l-xl first:border-l last:rounded-r-xl last:border-r group-hover/row:bg-accent", className)}
      {...props}
    />
  );
}

export { Table, TableBody, TableCell, TableHead, TableHeader, TableRow };
