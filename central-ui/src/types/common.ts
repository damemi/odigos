export interface PaginatedData<T = unknown> {
  nextPage: string;
  items: T[];
}

export interface DropdownOption<T = string> {
  id?: T | null;
  value?: string | null;
}
