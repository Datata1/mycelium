import { createHash } from "crypto";

/** Represents an authenticated user in the system. */
export interface User {
  id: string;
  email: string;
}

/** Hashes a password with SHA-256 for comparison against stored credentials. */
export function hashPassword(password: string): string {
  return createHash("sha256").update(password).digest("hex");
}

/** AuthService validates user credentials and issues tokens. */
export class AuthService {
  private secret: string;

  constructor(secret: string) {
    this.secret = secret;
  }

  /** Checks whether a token is valid for this service. */
  async validate(token: string): Promise<boolean> {
    return token.startsWith(this.secret);
  }
}
